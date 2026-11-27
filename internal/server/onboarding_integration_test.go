//go:build integration

package server_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/kibanyadev/dojo/internal/server"
)

// send is a JSON write as the browser: its cookies, plus the CSRF header when csrf is set.
func (b *browser) send(method, path, body string, csrf bool) (int, string) {
	b.t.Helper()
	req, _ := http.NewRequest(method, apiURL(b.t)+path, bytes.NewReader([]byte(body)))
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

func (b *browser) pathCookie() *http.Cookie {
	u, _ := url.Parse(apiURL(b.t) + "/v1/path")
	for _, c := range b.c.Jar.Cookies(u) {
		if c.Name == "dojo_path" {
			return c
		}
	}
	return nil
}

func (b *browser) mustSend(method, path, body string) {
	b.t.Helper()
	if code, out := b.send(method, path, body, true); code != http.StatusNoContent {
		b.t.Fatalf("%s %s: %d %q", method, path, code, out)
	}
}

func (b *browser) chosenPath() (string, bool) {
	b.t.Helper()
	resp, err := b.c.Get(apiURL(b.t) + "/v1/path")
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Path   *string `json:"path"`
		Chosen bool    `json:"chosen"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&out) != nil || out.Path == nil {
		b.t.Fatalf("GET /v1/path: %d", resp.StatusCode)
	}
	return *out.Path, out.Chosen
}

// seedExtraPath adds a second available path, so a choice can differ from the default.
func seedExtraPath(t *testing.T) {
	t.Helper()
	asOwner(t, "INSERT INTO paths (org_id, slug, title, status, position) SELECT id, 'extra', 'Extra', 'available', 3 FROM orgs WHERE slug = 'public'")
}

func chosenSlug(t *testing.T, githubID string) string {
	t.Helper()
	return asOwner(t, "SELECT coalesce((SELECT slug FROM paths WHERE id = l.chosen_path_id), 'none') FROM learners l WHERE github_id = "+githubID)
}

func waitlisted(t *testing.T, githubID string) string {
	t.Helper()
	return asOwner(t, "SELECT coalesce(string_agg(p.slug, ',' ORDER BY p.slug), '') FROM path_waitlist w JOIN paths p ON p.id = w.path_id JOIN learners l ON l.id = w.learner_id WHERE l.github_id = "+githubID)
}

func TestFirstSignInAppliesTheCookieChoice(t *testing.T) {
	freshCatalog(t)
	seedCatalog(t)
	seedExtraPath(t)
	resetLearners(t, "1001")
	b := newBrowser(t)
	b.mustSend("POST", "/v1/onboarding/waitlist", `{"path":"security"}`)
	b.mustSend("POST", "/v1/onboarding/path", `{"path":"extra"}`)
	if c := b.pathCookie(); c == nil {
		t.Fatal("no dojo_path cookie in the jar")
	}
	// Signed out the only reads are the cookie's; nothing is stored yet.
	resp := b.signIn("ada", "read:user", "age=on&terms=on", false)
	if resp.Header.Get("Location") != "/app" || b.sessionCookie(resp) == nil {
		t.Fatalf("callback: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if got := chosenSlug(t, "1001"); got != "extra" {
		t.Fatalf("chosen: %q", got)
	}
	// The learner has no email (the grant had no user:email) and still has the waitlist row.
	if got := asOwner(t, "SELECT email IS NULL FROM learners WHERE github_id = 1001"); got != "t" {
		t.Fatalf("email: %q", got)
	}
	if got := waitlisted(t, "1001"); got != "security" {
		t.Fatalf("waitlist: %q", got)
	}
	if b.pathCookie() != nil {
		t.Fatal("dojo_path survived the sign-in")
	}
	if path, chosen := b.chosenPath(); path != "extra" || !chosen {
		t.Fatalf("GET /v1/path: %q %v", path, chosen)
	}
}

func TestFirstSignInWithNoChoiceLeavesItNull(t *testing.T) {
	freshCatalog(t)
	seedCatalog(t)
	resetLearners(t, "1001")
	b := newBrowser(t)
	if resp := b.signIn("ada", "", "age=on&terms=on", false); resp.Header.Get("Location") != "/app" {
		t.Fatalf("callback: %q", resp.Header.Get("Location"))
	}
	if chosenSlug(t, "1001") != "none" || waitlisted(t, "1001") != "" {
		t.Fatalf("chosen %q waitlist %q", chosenSlug(t, "1001"), waitlisted(t, "1001"))
	}
	if path, chosen := b.chosenPath(); path != "systems-devops" || chosen {
		t.Fatalf("GET /v1/path: %q %v", path, chosen)
	}
}

func TestTamperedOrPlannedCookieChoiceIsIgnored(t *testing.T) {
	freshCatalog(t)
	seedCatalog(t)
	resetLearners(t, "1001", "1002")
	b := newBrowser(t)
	b.mustSend("POST", "/v1/onboarding/path", `{"path":"systems-devops"}`)
	c := b.pathCookie()
	u, _ := url.Parse(apiURL(t) + "/v1/")
	b.c.Jar.SetCookies(u, []*http.Cookie{{Name: "dojo_path", Value: "x" + c.Value[1:], Path: "/v1"}})
	if resp := b.signIn("ada", "", "age=on&terms=on", false); resp.Header.Get("Location") != "/app" {
		t.Fatalf("callback: %q", resp.Header.Get("Location"))
	}
	if chosenSlug(t, "1001") != "none" {
		t.Fatalf("a tampered cookie chose %q", chosenSlug(t, "1001"))
	}

	// A path that became planned, and a waitlist slug that became available, are skipped.
	b2 := newBrowser(t)
	b2.mustSend("POST", "/v1/onboarding/path", `{"path":"systems-devops"}`)
	b2.mustSend("POST", "/v1/onboarding/waitlist", `{"path":"security"}`)
	asOwner(t, "UPDATE paths SET status = CASE slug WHEN 'systems-devops' THEN 'planned' ELSE 'available' END")
	if resp := b2.signIn("bob", "", "age=on&terms=on", false); resp.Header.Get("Location") != "/app" {
		t.Fatalf("callback: %q", resp.Header.Get("Location"))
	}
	if chosenSlug(t, "1002") != "none" || waitlisted(t, "1002") != "" {
		t.Fatalf("chosen %q waitlist %q", chosenSlug(t, "1002"), waitlisted(t, "1002"))
	}
}

func TestReturningLearnerKeepsTheirChoice(t *testing.T) {
	freshCatalog(t)
	seedCatalog(t)
	seedExtraPath(t)
	resetLearners(t, "1001")
	first := newBrowser(t)
	first.mustSend("POST", "/v1/onboarding/path", `{"path":"extra"}`)
	if resp := first.signIn("ada", "", "age=on&terms=on", false); resp.Header.Get("Location") != "/app" {
		t.Fatalf("first: %q", resp.Header.Get("Location"))
	}

	// A stale cookie on a later sign-in never overwrites the choice, but a missing waitlist row is added.
	again := newBrowser(t)
	again.mustSend("POST", "/v1/onboarding/path", `{"path":"systems-devops"}`)
	again.mustSend("POST", "/v1/onboarding/waitlist", `{"path":"security"}`)
	if resp := again.signIn("ada", "", "", false); resp.Header.Get("Location") != "/app" {
		t.Fatalf("again: %q", resp.Header.Get("Location"))
	}
	if got := chosenSlug(t, "1001"); got != "extra" {
		t.Fatalf("overwritten: %q", got)
	}
	if got := waitlisted(t, "1001"); got != "security" {
		t.Fatalf("waitlist: %q", got)
	}

	// With no choice stored, the cookie's path is applied.
	asOwner(t, "UPDATE learners SET chosen_path_id = NULL WHERE github_id = 1001")
	third := newBrowser(t)
	third.mustSend("POST", "/v1/onboarding/path", `{"path":"systems-devops"}`)
	if resp := third.signIn("ada", "", "", false); resp.Header.Get("Location") != "/app" {
		t.Fatalf("third: %q", resp.Header.Get("Location"))
	}
	if got := chosenSlug(t, "1001"); got != "systems-devops" {
		t.Fatalf("applied: %q", got)
	}
}

func TestOnboardingWritesSignedInAndErrors(t *testing.T) {
	freshCatalog(t)
	seedCatalog(t)
	seedExtraPath(t)
	resetLearners(t, "1001", "1002")
	ada := newBrowser(t)
	if resp := ada.signIn("ada", "", "age=on&terms=on", false); resp.Header.Get("Location") != "/app" {
		t.Fatalf("sign-in: %q", resp.Header.Get("Location"))
	}
	if code, _ := ada.send("POST", "/v1/onboarding/path", `{"path":"extra"}`, false); code != http.StatusForbidden {
		t.Fatalf("without CSRF: %d", code)
	}
	if chosenSlug(t, "1001") != "none" {
		t.Fatal("a refused call chose a path")
	}
	ada.mustSend("POST", "/v1/onboarding/path", `{"path":"extra"}`)
	ada.mustSend("POST", "/v1/onboarding/path", `{"path":"systems-devops"}`)
	if chosenSlug(t, "1001") != "systems-devops" || ada.pathCookie() != nil {
		t.Fatalf("switch: %q cookie=%v", chosenSlug(t, "1001"), ada.pathCookie())
	}
	ada.mustSend("POST", "/v1/onboarding/waitlist", `{"path":"security"}`)
	ada.mustSend("POST", "/v1/onboarding/waitlist", `{"path":"security"}`)
	if waitlisted(t, "1001") != "security" {
		t.Fatalf("waitlist: %q", waitlisted(t, "1001"))
	}
	ada.mustSend("DELETE", "/v1/onboarding/waitlist/security", "")
	if waitlisted(t, "1001") != "" {
		t.Fatalf("waitlist after delete: %q", waitlisted(t, "1001"))
	}

	// The refusals run as a second learner: they spend the write budget (10 a minute) like any write.
	bob := newBrowser(t)
	if resp := bob.signIn("bob", "", "age=on&terms=on", false); resp.Header.Get("Location") != "/app" {
		t.Fatalf("bob sign-in: %q", resp.Header.Get("Location"))
	}
	for _, tt := range []struct {
		method, path, body, code string
		status                   int
	}{
		{"POST", "/v1/onboarding/path", `{"path":"nope"}`, "not_found", 404},
		{"POST", "/v1/onboarding/path", `{"path":"security"}`, "path_planned", 409},
		{"POST", "/v1/onboarding/path", `{}`, "bad_request", 400},
		{"POST", "/v1/onboarding/waitlist", `{"path":"extra"}`, "path_available", 409},
		{"DELETE", "/v1/onboarding/waitlist/nope", "", "not_found", 404},
		{"DELETE", "/v1/onboarding/waitlist/extra", "", "path_available", 409},
	} {
		code, body := bob.send(tt.method, tt.path, tt.body, true)
		if code != tt.status || !strings.Contains(body, `"`+tt.code+`"`) {
			t.Fatalf("%s %s %s: %d %q", tt.method, tt.path, tt.body, code, body)
		}
	}

	// A bearer call needs the same JSON and header, and writes the learner's row.
	tok := cliToken(t, newCLIClient(t), "ada")
	c := newCLIClient(t)
	if code, _ := c.call("POST", "/v1/onboarding/path", tok, map[string]any{"path": "extra"}); code != http.StatusForbidden {
		t.Fatalf("bearer without the header: %d", code)
	}
	req, _ := http.NewRequest("POST", apiURL(t)+"/v1/onboarding/path", strings.NewReader(`{"path":"extra"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(server.CSRFHeader, "1")
	req.Header.Set("Authorization", "Bearer "+tok)
	if resp := c.b.do(req); resp.StatusCode != http.StatusNoContent || chosenSlug(t, "1001") != "extra" {
		t.Fatalf("bearer: %d %q", resp.StatusCode, chosenSlug(t, "1001"))
	}
}

func TestOnboardingSignedOutWritesAreLimitedPerIP(t *testing.T) {
	freshCatalog(t)
	seedCatalog(t)
	b := newBrowser(t)
	limited := 0
	for i := 0; i < 35; i++ {
		code, _ := b.send("POST", "/v1/onboarding/path", `{"path":"systems-devops"}`, true)
		switch code {
		case http.StatusTooManyRequests:
			limited++
		case http.StatusNoContent:
		default:
			t.Fatalf("call %d: %d", i, code)
		}
	}
	if limited != 5 {
		t.Fatalf("%d of 35 limited, want 5 past the burst of 30", limited)
	}
}
