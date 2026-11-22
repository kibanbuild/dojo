//go:build integration

package server_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// One smoke test of the fake GitHub service: the web flow end to end, then revoke.
func TestFakeGitHubWebFlow(t *testing.T) {
	base := os.Getenv("DOJO_FAKE_GITHUB_URL")
	if base == "" {
		t.Fatal("DOJO_FAKE_GITHUB_URL is not set: run `make test-integration`")
	}
	const cb = "http://localhost:8080/v1/auth/github/callback"
	verifier := "integration-verifier-0123456789-abcdefghijklmnop"
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{"client_id": {"dojo-web-local"}, "redirect_uri": {cb}, "scope": {"read:user user:email"},
		"state": {"s"}, "login": {"ada"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"}}
	c := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	resp, err := c.Get(base + "/login/oauth/authorize?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if resp.StatusCode != http.StatusFound || err != nil || loc.Query().Get("code") == "" {
		t.Fatalf("authorize: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}

	form := url.Values{"client_id": {"dojo-web-local"}, "client_secret": {"web-local-only"}, "code": {loc.Query().Get("code")}, "code_verifier": {verifier}}
	req, _ := http.NewRequest(http.MethodPost, base+"/login/oauth/access_token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err = c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	err = json.NewDecoder(resp.Body).Decode(&tok)
	resp.Body.Close()
	if err != nil || tok.AccessToken == "" {
		t.Fatalf("access_token: %v %+v", err, tok)
	}

	userStatus := func() (int, map[string]any) {
		req, _ := http.NewRequest(http.MethodGet, base+"/user", nil)
		req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var u map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&u)
		return resp.StatusCode, u
	}
	if code, u := userStatus(); code != 200 || u["login"] != "ada" || u["two_factor_authentication"] != true {
		t.Fatalf("user: %d %v", code, u)
	}

	req, _ = http.NewRequest(http.MethodDelete, base+"/applications/dojo-web-local/token", strings.NewReader(`{"access_token":"`+tok.AccessToken+`"}`))
	req.SetBasicAuth("dojo-web-local", "web-local-only")
	resp, err = c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke: %d", resp.StatusCode)
	}
	if code, _ := userStatus(); code != http.StatusUnauthorized {
		t.Fatalf("after revoke: %d", code)
	}
}

// The device flow against the running fake: code, approve through the test hook, poll, check-token, revoke.
func TestFakeGitHubDeviceFlow(t *testing.T) {
	base := os.Getenv("DOJO_FAKE_GITHUB_URL")
	if base == "" {
		t.Fatal("DOJO_FAKE_GITHUB_URL is not set: run `make test-integration`")
	}
	c := &http.Client{Timeout: 10 * time.Second}
	post := func(path string, form url.Values) map[string]any {
		req, _ := http.NewRequest(http.MethodPost, base+path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return out
	}
	d := post("/login/device/code", url.Values{"client_id": {"dojo-web-local"}, "scope": {"read:user user:email"}})
	dc, _ := d["device_code"].(string)
	uc, _ := d["user_code"].(string)
	if dc == "" || uc == "" || d["interval"] == nil {
		t.Fatalf("device/code: %v", d)
	}
	// The API reaches the fake as fake-github, but the learner's browser opens verification_uri: it must be host-reachable.
	if uri, _ := d["verification_uri"].(string); !strings.HasPrefix(uri, "http://localhost:") || !strings.HasSuffix(uri, "/login/device") || strings.Contains(uri, "fake-github") {
		t.Fatalf("verification_uri %q is not host-reachable", uri)
	}
	poll := url.Values{"client_id": {"dojo-web-local"}, "device_code": {dc}, "grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}}
	// The stack's poll interval is 1s: the first poll is pending, a second one straight after is told to slow down.
	if out := post("/login/oauth/access_token", poll); out["error"] != "authorization_pending" {
		t.Fatalf("first poll: %v", out)
	}
	if out := post("/login/oauth/access_token", poll); out["error"] != "slow_down" {
		t.Fatalf("early poll: %v", out)
	}
	resp, err := c.PostForm(base+"/login/device", url.Values{"user_code": {uc}, "login": {"ada"}, "decision": {"approve"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("approve: %d", resp.StatusCode)
	}
	var out map[string]any
	for i := 0; i < 8; i++ { // slow_down bumped the interval to 6s
		time.Sleep(7 * time.Second)
		if out = post("/login/oauth/access_token", poll); out["error"] == nil {
			break
		}
		if out["error"] != "slow_down" && out["error"] != "authorization_pending" {
			t.Fatalf("poll: %v", out)
		}
	}
	tok, _ := out["access_token"].(string)
	if tok == "" {
		t.Fatalf("no token: %v", out)
	}
	check := func(method string) int {
		req, _ := http.NewRequest(method, base+"/applications/dojo-web-local/token", strings.NewReader(`{"access_token":"`+tok+`"}`))
		req.SetBasicAuth("dojo-web-local", "web-local-only")
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := check(http.MethodPost); got != 200 {
		t.Fatalf("check-token: %d", got)
	}
	if got := check(http.MethodDelete); got != 204 {
		t.Fatalf("revoke: %d", got)
	}
	if got := check(http.MethodPost); got != 404 {
		t.Fatalf("check-token after revoke: %d", got)
	}
}
