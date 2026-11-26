//go:build integration

package server_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

func emailSourceOf(t *testing.T, gh string) string {
	t.Helper()
	return asOwner(t, "SELECT coalesce(email_source, 'none') FROM learners WHERE github_id = "+gh)
}

func e1Rows(t *testing.T, gh string) string {
	t.Helper()
	return asOwner(t, "SELECT count(*) FROM email_outbox WHERE kind = 'E1' AND ref = (SELECT id::text FROM learners WHERE github_id = "+gh+")")
}

func TestSignInSetsEmailSource(t *testing.T) {
	resetLearners(t, "1001", "1002")
	t.Cleanup(func() { idle(t) })
	idle(t)
	signUp(t, "ada")
	if got := emailSourceOf(t, "1001"); got != "github" {
		t.Fatalf("github email: %s", got)
	}
	if resp := newBrowser(t).signIn("bob", "read:user", "age=on&terms=on", false); resp.Header.Get("Location") != "/app" {
		t.Fatalf("bob: %q", resp.Header.Get("Location"))
	}
	if got := emailSourceOf(t, "1002"); got != "none" {
		t.Fatalf("no email: %s", got)
	}
}

func TestEmailPromptSavesOnceAndQueuesOneWelcome(t *testing.T) {
	const addr = "bob-self@example.test"
	resetLearners(t, "1002")
	t.Cleanup(func() { idle(t) })
	idle(t)
	clearInbox(t)

	b := newBrowser(t)
	if resp := b.signIn("bob", "read:user", "age=on&terms=on", false); resp.Header.Get("Location") != "/app" {
		t.Fatalf("sign-in: %q", resp.Header.Get("Location"))
	}
	if _, me := b.me(); me["email"] != nil {
		t.Fatalf("a learner with no address: %v", me)
	}
	if code, body := b.patch(`{"email":"not an address"}`, true); code != http.StatusBadRequest || body != `{"error":"invalid_email"}`+"\n" {
		t.Fatalf("invalid: %d %q", code, body)
	}
	if code, body := b.patch(`{"email":"`+addr+`"}`, true); code != http.StatusOK {
		t.Fatalf("save: %d %q", code, body)
	}
	if _, me := b.me(); me["email"] != addr {
		t.Fatalf("me: %v", me)
	}
	if got := emailSourceOf(t, "1002"); got != "learner" {
		t.Fatalf("source: %s", got)
	}
	if got := e1Rows(t, "1002"); got != "1" {
		t.Fatalf("E1 rows: %s", got)
	}
	waitFor(t, "the welcome mail", func() bool { return len(inboxOf(t, addr)) > 0 })
	if msgs := inboxOf(t, addr); len(msgs) != 1 || msgs[0].Subject != "Welcome to Dojo" {
		t.Fatalf("inbox: %+v", msgs)
	}

	// A second address is refused and queues nothing.
	if code, body := b.patch(`{"email":"other@example.test"}`, true); code != http.StatusConflict || body != `{"error":"email_set"}`+"\n" {
		t.Fatalf("second: %d %q", code, body)
	}
	if got := e1Rows(t, "1002"); got != "1" {
		t.Fatalf("E1 rows after the second save: %s", got)
	}

	// A GitHub email at a later sign-in replaces the learner's and takes the source back; E1 is not queued again.
	if resp := newBrowser(t).signIn("bob", "", "", false); resp.Header.Get("Location") != "/app" {
		t.Fatalf("later sign-in: %q", resp.Header.Get("Location"))
	}
	if got := emailSourceOf(t, "1002"); got != "github" {
		t.Fatalf("source after the GitHub email: %s", got)
	}
	if got := e1Rows(t, "1002"); got != "1" {
		t.Fatalf("E1 rows after the later sign-in: %s", got)
	}
}

func TestLaterSignInThatBringsTheFirstAddressQueuesWelcome(t *testing.T) {
	const addr = "ada@example.test"
	resetLearners(t, "1001")
	t.Cleanup(func() { idle(t) })
	idle(t)
	clearInbox(t)

	if resp := newBrowser(t).signIn("ada", "read:user", "age=on&terms=on", false); resp.Header.Get("Location") != "/app" {
		t.Fatalf("first sign-in: %q", resp.Header.Get("Location"))
	}
	if got := e1Rows(t, "1001"); got != "0" {
		t.Fatalf("E1 rows with no address: %s", got)
	}
	if resp := newBrowser(t).signIn("ada", "", "", false); resp.Header.Get("Location") != "/app" {
		t.Fatalf("second sign-in: %q", resp.Header.Get("Location"))
	}
	if got := e1Rows(t, "1001"); got != "1" {
		t.Fatalf("E1 rows once the address arrives: %s", got)
	}
	waitFor(t, "the welcome mail", func() bool { return len(inboxOf(t, addr)) > 0 })
	if resp := newBrowser(t).signIn("ada", "", "", false); resp.Header.Get("Location") != "/app" {
		t.Fatalf("third sign-in: %q", resp.Header.Get("Location"))
	}
	if got := e1Rows(t, "1001"); got != "1" {
		t.Fatalf("E1 rows after a third sign-in: %s", got)
	}
}

func TestFirstRunOnTodayUntilSeen(t *testing.T) {
	freshCatalog(t)
	seedCatalog(t)
	resetLearners(t, "1001")
	t.Cleanup(func() { idle(t) })
	signUp(t, "ada")
	c := newCLIClient(t)
	tok := cliToken(t, c, "ada")

	firstRun := func() map[string]any {
		t.Helper()
		code, body, _ := getAs(t, tok, "/v1/today")
		var out struct {
			FirstRun map[string]any `json:"first_run"`
		}
		if code != http.StatusOK || json.Unmarshal([]byte(body), &out) != nil {
			t.Fatalf("today: %d %s", code, body)
		}
		return out.FirstRun
	}
	if got := firstRun(); got != nil {
		t.Fatalf("no pass yet: %v", got)
	}
	setProgress(t, "foundations/a", "started", "running")
	if got := firstRun(); got != nil {
		t.Fatalf("a started ticket is not a pass: %v", got)
	}

	asOwner(t, "UPDATE ticket_progress SET state = 'passed', first_passed_at = now() - interval '1 hour' WHERE item_id = 'foundations/a'")
	setProgress(t, "foundations/b", "passed", "stopped")
	got := firstRun()
	if got == nil || got["id"] != "foundations/a" || got["title"] == "" || got["first_passed_at"] == "" || got["passed_commit_state"] != nil {
		t.Fatalf("earliest pass: %v", got)
	}
	asOwner(t, "UPDATE ticket_progress SET commit_state = 'pending' WHERE item_id = 'foundations/a'")
	if got := firstRun(); got == nil || got["passed_commit_state"] != "pending" {
		t.Fatalf("commit state: %v", got)
	}

	if code, body := c.call(http.MethodPatch, "/v1/me", tok, map[string]any{"first_run_seen": false}); code != http.StatusBadRequest {
		t.Fatalf("false: %d %q", code, body)
	}
	if got := firstRun(); got == nil {
		t.Fatal("a refused patch cleared the first run")
	}
	if code, body := c.call(http.MethodPatch, "/v1/me", tok, map[string]any{"first_run_seen": true}); code != http.StatusOK {
		t.Fatalf("seen: %d %q", code, body)
	}
	if got := firstRun(); got != nil {
		t.Fatalf("after seen: %v", got)
	}
	at := asOwner(t, "SELECT first_run_seen_at FROM learners WHERE github_id = 1001")
	if code, body := c.call(http.MethodPatch, "/v1/me", tok, map[string]any{"first_run_seen": true}); code != http.StatusOK {
		t.Fatalf("repeat: %d %q", code, body)
	}
	if again := asOwner(t, "SELECT first_run_seen_at FROM learners WHERE github_id = 1001"); again != at || at == "" {
		t.Fatalf("a repeat changed first_run_seen_at: %q then %q", at, again)
	}
}
