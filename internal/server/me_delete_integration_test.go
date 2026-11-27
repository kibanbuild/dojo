//go:build integration

package server_test

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/kibanyadev/dojo/internal/server"
)

// eraseMe sends DELETE /v1/me as the browser: its cookie, plus the CSRF header when csrf is set.
func (b *browser) eraseMe(body string, csrf bool) (int, string, *http.Response) {
	b.t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, apiURL(b.t)+"/v1/me", bytes.NewReader([]byte(body)))
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
	return resp.StatusCode, string(out), resp
}

func TestDeleteMeErasesTheAccount(t *testing.T) {
	resetLearners(t, "1001", "1002")
	ada, bob := newBrowser(t), newBrowser(t)
	if resp := ada.signIn("ada", "", "age=on&terms=on", false); resp.Header.Get("Location") != "/app" {
		t.Fatalf("ada sign-in: %q", resp.Header.Get("Location"))
	}
	if resp := bob.signIn("bob", "", "age=on&terms=on", false); resp.Header.Get("Location") != "/app" {
		t.Fatalf("bob sign-in: %q", resp.Header.Get("Location"))
	}
	adaID := strings.TrimSpace(asOwner(t, "SELECT id FROM learners WHERE github_id = 1001"))
	bobID := strings.TrimSpace(asOwner(t, "SELECT id FROM learners WHERE github_id = 1002"))
	t.Cleanup(func() { asOwner(t, "DELETE FROM email_outbox WHERE kind = 'E8' AND ref IN ('"+adaID+"','"+bobID+"')") })

	c := newCLIClient(t)
	tok := cliToken(t, c, "ada")
	if n := countWhere(t, "cli_tokens", "learner_id = '"+adaID+"'"); n != "1" {
		t.Fatalf("ada tokens before: %s", n)
	}

	// A CLI token is valid but may not delete an account; nothing changes.
	if code, body := c.call(http.MethodDelete, "/v1/me", tok, map[string]string{"confirm": "ada"}); code != http.StatusForbidden || body != "{\"error\":\"forbidden\"}\n" {
		t.Fatalf("bearer: %d %q", code, body)
	}
	if learnerCount(t, "1001") != "1" {
		t.Fatal("a bearer call deleted the learner")
	}

	// Refusals change nothing: no CSRF header, wrong confirmation, unknown field.
	if code, _, _ := ada.eraseMe(`{"confirm":"ada"}`, false); code != http.StatusForbidden {
		t.Fatalf("without CSRF: %d", code)
	}
	if code, body, _ := ada.eraseMe(`{"confirm":"bob"}`, true); code != http.StatusBadRequest || body != "{\"error\":\"confirm_mismatch\"}\n" {
		t.Fatalf("mismatch: %d %q", code, body)
	}
	if code, _, _ := ada.eraseMe(`{"confirm":"ada","x":1}`, true); code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", code)
	}
	if code, _ := ada.me(); code != http.StatusOK || learnerCount(t, "1001") != "1" {
		t.Fatalf("a refused delete changed something: %d", code)
	}

	// The right confirmation, in any case: 204 and the cookie is cleared.
	code, body, resp := ada.eraseMe(`{"confirm":"ADA"}`, true)
	if code != http.StatusNoContent || body != "" {
		t.Fatalf("delete: %d %q", code, body)
	}
	if c := ada.sessionCookie(resp); c == nil || c.MaxAge >= 0 {
		t.Fatalf("session cookie not cleared: %+v", c)
	}
	if code, _ := ada.me(); code != http.StatusUnauthorized {
		t.Fatalf("me after delete: %d", code)
	}
	if code, _ := c.call(http.MethodGet, "/v1/me", tok, nil); code != http.StatusUnauthorized {
		t.Fatalf("cli token after delete: %d", code)
	}

	// The row and everything that hangs off it is gone; exactly one E8 remains.
	if n := countWhere(t, "learners", "id = '"+adaID+"'"); n != "0" {
		t.Fatalf("learner rows: %s", n)
	}
	if n := countWhere(t, "sessions", "learner_id = '"+adaID+"'"); n != "0" {
		t.Fatalf("session rows: %s", n)
	}
	if n := countWhere(t, "cli_tokens", "learner_id = '"+adaID+"'"); n != "0" {
		t.Fatalf("cli token rows: %s", n)
	}
	if n := countWhere(t, "email_outbox", "kind = 'E8' AND ref = '"+adaID+"'"); n != "1" {
		t.Fatalf("E8 rows: %s, want 1", n)
	}

	// A second delete is no-op: the session is gone, so it is 401 and queues no second E8.
	if code, _, _ := ada.eraseMe(`{"confirm":"ada"}`, true); code != http.StatusUnauthorized {
		t.Fatalf("repeat: %d", code)
	}
	if n := countWhere(t, "email_outbox", "kind = 'E8' AND ref = '"+adaID+"'"); n != "1" {
		t.Fatalf("E8 rows after repeat: %s", n)
	}

	// The other learner is untouched, and a suspended learner may still erase themselves.
	if code, me := bob.me(); code != http.StatusOK || me["username"] != "bob" {
		t.Fatalf("bob: %d %v", code, me)
	}
	if n := countWhere(t, "sessions", "learner_id = '"+bobID+"'"); n != "1" {
		t.Fatalf("bob sessions: %s", n)
	}
	if n := countWhere(t, "email_outbox", "kind = 'E8' AND ref = '"+bobID+"'"); n != "0" {
		t.Fatalf("bob E8 rows: %s", n)
	}
	asOwner(t, "UPDATE learners SET suspended_at = now() WHERE github_id = 1002")
	if code, _, _ := bob.eraseMe(`{"confirm":"Bob"}`, true); code != http.StatusNoContent {
		t.Fatalf("suspended delete: %d", code)
	}
	if learnerCount(t, "1002") != "0" {
		t.Fatal("suspended learner still there")
	}
}
