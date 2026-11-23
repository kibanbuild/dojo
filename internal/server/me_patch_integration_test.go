//go:build integration

package server_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/kibanbuild/dojo/internal/server"
)

// patch sends PATCH /v1/me as the browser: its cookie, plus the CSRF headers when csrf is set.
func (b *browser) patch(body string, csrf bool) (int, string) {
	b.t.Helper()
	req, _ := http.NewRequest(http.MethodPatch, apiURL(b.t)+"/v1/me", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	if csrf {
		req.Header.Set(server.CSRFHeader, "1")
	}
	resp, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

func cliToken(t *testing.T, c *cliClient, login string) string {
	t.Helper()
	code, body := c.call(http.MethodPost, "/v1/auth/cli", "", cliLoginBody(deviceToken(t, login, "read:user user:email")))
	var out struct{ Token string }
	if code != http.StatusCreated || json.Unmarshal([]byte(body), &out) != nil {
		t.Fatalf("cli login %s: %d %q", login, code, body)
	}
	return out.Token
}

func TestPatchMeRenameAndLanguage(t *testing.T) {
	resetLearners(t, "1001", "1002")
	ada, bob := newBrowser(t), newBrowser(t)
	if resp := ada.signIn("ada", "", "age=on&terms=on", false); resp.Header.Get("Location") != "/app" {
		t.Fatalf("ada sign-in: %q", resp.Header.Get("Location"))
	}
	if resp := bob.signIn("bob", "", "age=on&terms=on", false); resp.Header.Get("Location") != "/app" {
		t.Fatalf("bob sign-in: %q", resp.Header.Get("Location"))
	}
	before := asOwner(t, "SELECT updated_at FROM learners WHERE github_id = 1001")

	// CSRF is required for the cookie, and a refusal changes nothing.
	if code, _ := ada.patch(`{"username":"ada-new"}`, false); code != http.StatusForbidden {
		t.Fatalf("without CSRF: %d", code)
	}
	if _, me := ada.me(); me["username"] != "ada" {
		t.Fatalf("a refused patch renamed: %v", me)
	}

	// Rename: the answer is the new /v1/me, the next GET agrees, updated_at moved, github_login did not.
	code, body := ada.patch(`{"username":"Ada-New","language":"es"}`, true)
	var got map[string]any
	if code != http.StatusOK || json.Unmarshal([]byte(body), &got) != nil || got["username"] != "ada-new" || got["language"] != "es" || got["github_login"] != "ada" {
		t.Fatalf("rename: %d %q", code, body)
	}
	if _, me := ada.me(); me["username"] != "ada-new" || me["language"] != "es" {
		t.Fatalf("me after rename: %v", me)
	}
	if after := asOwner(t, "SELECT updated_at FROM learners WHERE github_id = 1001"); after == before {
		t.Fatalf("updated_at did not move: %s", after)
	}

	// A no-op is fine, and so is keeping the current name while changing the language.
	if code, _ := ada.patch(`{"username":"ada-new","language":"en"}`, true); code != http.StatusOK {
		t.Fatalf("no-op: %d", code)
	}
	if _, me := ada.me(); me["language"] != "en" {
		t.Fatalf("language back: %v", me)
	}

	// Taking another learner's name is 409, in any case, and keeps the old name.
	for _, name := range []string{"bob", "BOB"} {
		if code, body := ada.patch(`{"username":"`+name+`"}`, true); code != http.StatusConflict || body != "{\"error\":\"username_taken\"}\n" {
			t.Fatalf("take %q: %d %q", name, code, body)
		}
	}
	if _, me := ada.me(); me["username"] != "ada-new" {
		t.Fatalf("a conflict renamed: %v", me)
	}

	for _, tt := range []struct{ body, want string }{
		{`{"username":"admin"}`, "invalid_username"},
		{`{"username":"ab"}`, "invalid_username"},
		{`{"username":"has space"}`, "invalid_username"},
		{`{"language":"fr"}`, "invalid_language"},
		{`{}`, "bad_request"},
		{`{"email":"x@y.z"}`, "bad_request"},
	} {
		if code, body := ada.patch(tt.body, true); code != http.StatusBadRequest || body != "{\"error\":\""+tt.want+"\"}\n" {
			t.Fatalf("%s: %d %q", tt.body, code, body)
		}
	}
	if n := asOwner(t, "SELECT count(*) FROM learners WHERE username = 'admin'"); n != "0" {
		t.Fatalf("reserved name stored: %s", n)
	}

	// Another learner is untouched by all of it.
	if _, me := bob.me(); me["username"] != "bob" || me["language"] != "en" {
		t.Fatalf("bob changed: %v", me)
	}
	if code, _ := (&browser{t: t, c: &http.Client{Transport: ipTransport{"10.99.0.1"}}}).patch(`{"username":"zed"}`, true); code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", code)
	}
}

func TestPatchMeWithBearer(t *testing.T) {
	resetLearners(t, "1001", "1002")
	signUp(t, "ada")
	signUp(t, "bob")
	c := newCLIClient(t)
	tok := cliToken(t, c, "ada")

	// A bearer needs no CSRF header.
	code, body := c.call(http.MethodPatch, "/v1/me", tok, map[string]string{"username": "ada-cli", "language": "es"})
	var got map[string]any
	if code != http.StatusOK || json.Unmarshal([]byte(body), &got) != nil || got["username"] != "ada-cli" || got["language"] != "es" {
		t.Fatalf("patch with bearer: %d %q", code, body)
	}
	if code, body := c.call(http.MethodGet, "/v1/me", tok, nil); code != http.StatusOK || !strings.Contains(body, `"username":"ada-cli"`) {
		t.Fatalf("me after: %d %q", code, body)
	}
	if code, body := c.call(http.MethodPatch, "/v1/me", tok, map[string]string{"username": "bob"}); code != http.StatusConflict || body != "{\"error\":\"username_taken\"}\n" {
		t.Fatalf("take bob: %d %q", code, body)
	}
	if code, _ := c.call(http.MethodPatch, "/v1/me", tok, map[string]string{"username": "root"}); code != http.StatusBadRequest {
		t.Fatalf("reserved: %d", code)
	}
	if code, _ := c.call(http.MethodPatch, "/v1/me", "", map[string]string{"username": "zed"}); code != http.StatusUnauthorized {
		t.Fatalf("no credential: %d", code)
	}

	asOwner(t, "UPDATE learners SET suspended_at = now() WHERE github_id = 1001")
	if code, body := c.call(http.MethodPatch, "/v1/me", tok, map[string]string{"username": "ada-again"}); code != http.StatusForbidden || body != "{\"error\":\"account_suspended\"}\n" {
		t.Fatalf("suspended: %d %q", code, body)
	}
	if n := asOwner(t, "SELECT username FROM learners WHERE github_id = 1001"); n != "ada-cli" {
		t.Fatalf("suspended learner renamed: %s", n)
	}
}

func TestPatchMeUsageStats(t *testing.T) {
	resetLearners(t, "1001")
	ada := newBrowser(t)
	if resp := ada.signIn("ada", "", "age=on&terms=on", false); resp.Header.Get("Location") != "/app" {
		t.Fatalf("ada sign-in: %q", resp.Header.Get("Location"))
	}
	if _, me := ada.me(); me["usage_stats"] != true {
		t.Fatalf("default: %v", me)
	}
	if code, _ := ada.patch(`{"usage_stats":false}`, false); code != http.StatusForbidden {
		t.Fatalf("without CSRF: %d", code)
	}
	code, body := ada.patch(`{"usage_stats":false}`, true)
	var got map[string]any
	if code != http.StatusOK || json.Unmarshal([]byte(body), &got) != nil || got["usage_stats"] != false {
		t.Fatalf("cookie off: %d %q", code, body)
	}
	if _, me := ada.me(); me["usage_stats"] != false || me["username"] != "ada" || me["language"] != "en" {
		t.Fatalf("me after: %v", me)
	}
	for _, bad := range []string{`{"usage_stats":"false"}`, `{"usage_stats":0}`, `{"usage_stats":null}`} {
		if code, _ := ada.patch(bad, true); code != http.StatusBadRequest {
			t.Fatalf("%s: %d", bad, code)
		}
	}
	if _, me := ada.me(); me["usage_stats"] != false {
		t.Fatalf("a refused patch changed it: %v", me)
	}

	c := newCLIClient(t)
	tok := cliToken(t, c, "ada")
	if code, body := c.call(http.MethodPatch, "/v1/me", tok, map[string]bool{"usage_stats": true}); code != http.StatusOK || !strings.Contains(body, `"usage_stats":true`) {
		t.Fatalf("bearer on: %d %q", code, body)
	}
	if _, me := ada.me(); me["usage_stats"] != true {
		t.Fatalf("cookie sees: %v", me)
	}
}

func TestPatchMeAnalyticsConsent(t *testing.T) {
	resetLearners(t, "1001")
	ada := newBrowser(t)
	if resp := ada.signIn("ada", "", "age=on&terms=on", false); resp.Header.Get("Location") != "/app" {
		t.Fatalf("ada sign-in: %q", resp.Header.Get("Location"))
	}
	if _, me := ada.me(); me["analytics_consent"] != nil {
		t.Fatalf("default: %v", me)
	}
	if code, _ := ada.patch(`{"analytics_consent":true}`, false); code != http.StatusForbidden {
		t.Fatalf("without CSRF: %d", code)
	}
	for _, want := range []bool{true, false} {
		code, body := ada.patch(`{"analytics_consent":`+fmt.Sprint(want)+`}`, true)
		var got map[string]any
		if code != http.StatusOK || json.Unmarshal([]byte(body), &got) != nil || got["analytics_consent"] != want {
			t.Fatalf("set %v: %d %q", want, code, body)
		}
		if _, me := ada.me(); me["analytics_consent"] != want || me["usage_stats"] != true {
			t.Fatalf("me after %v: %v", want, me)
		}
	}
	for _, bad := range []string{`{"analytics_consent":null}`, `{"analytics_consent":"true"}`, `{"analytics_consent":1}`} {
		if code, _ := ada.patch(bad, true); code != http.StatusBadRequest {
			t.Fatalf("%s: %d", bad, code)
		}
	}
	if _, me := ada.me(); me["analytics_consent"] != false {
		t.Fatalf("a refused patch changed it: %v", me)
	}
}

// signInWithConsentCookie signs in with the web app's consent cookie in the jar ("" sends none).
func signInWithConsentCookie(t *testing.T, login, cookie, ticks string) *browser {
	t.Helper()
	b := newBrowser(t)
	if cookie != "" {
		u, _ := url.Parse(apiURL(t))
		b.c.Jar.SetCookies(u, []*http.Cookie{{Name: "dojo_analytics_consent", Value: cookie, Path: "/"}})
	}
	if resp := b.signIn(login, "", ticks, false); resp.Header.Get("Location") != "/app" {
		t.Fatalf("%s sign-in: %q", login, resp.Header.Get("Location"))
	}
	return b
}

func TestWebSignInCopiesTheConsentCookie(t *testing.T) {
	consent := func(t *testing.T) string {
		return asOwner(t, "SELECT coalesce(analytics_consent::text, 'null') FROM learners WHERE github_id = 1001")
	}
	const ticks = "age=on&terms=on"
	tests := []struct {
		name, first, second, wantFirst, wantSecond string
	}{
		{"new learner with granted", "granted", "", "true", "true"},
		{"new learner with denied", "denied", "", "false", "false"},
		{"new learner with garbage", "maybe", "", "null", "null"},
		{"returning, null, granted", "", "granted", "null", "true"},
		{"returning, null, denied", "", "denied", "null", "false"},
		{"returning, null, garbage", "", "Granted", "null", "null"},
		{"returning, false, granted stays false", "denied", "granted", "false", "false"},
		{"returning, true, denied stays true", "granted", "denied", "true", "true"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetLearners(t, "1001")
			signInWithConsentCookie(t, "ada", tt.first, ticks)
			if got := consent(t); got != tt.wantFirst {
				t.Fatalf("after first sign-in: %s, want %s", got, tt.wantFirst)
			}
			b := signInWithConsentCookie(t, "ada", tt.second, "")
			if got := consent(t); got != tt.wantSecond {
				t.Fatalf("after second sign-in: %s, want %s", got, tt.wantSecond)
			}
			want := map[string]any{"true": true, "false": false, "null": nil}[tt.wantSecond]
			if _, me := b.me(); me["analytics_consent"] != want {
				t.Fatalf("me: %v, want %v", me["analytics_consent"], want)
			}
		})
	}
}
