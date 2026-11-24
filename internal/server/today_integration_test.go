//go:build integration

package server_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

type todayBody struct {
	Current, Next *struct {
		ID       string
		State    *string
		LabState *string `json:"lab_state"`
	}
	Role *struct {
		Slug          string
		TicketsPassed int `json:"tickets_passed"`
		TicketsTotal  int `json:"tickets_total"`
	}
	AllPassed bool `json:"all_passed"`
}

func today(t *testing.T, tok string) todayBody {
	t.Helper()
	code, body, h := getAs(t, tok, "/v1/today")
	var got todayBody
	if err := json.Unmarshal([]byte(body), &got); err != nil || code != 200 || h.Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %v %q %s", code, err, h.Get("Cache-Control"), body)
	}
	return got
}

func TestTodayIntegration(t *testing.T) {
	freshCatalog(t)
	seedCatalog(t)
	resetLearners(t, "1001", "1002")
	signUp(t, "ada")
	signUp(t, "bob")
	tok := cliToken(t, newCLIClient(t), "ada")
	bobTok := cliToken(t, newCLIClient(t), "bob")

	got := today(t, tok)
	if got.Current != nil || got.Next == nil || got.Next.ID != "foundations/a" || got.Next.State != nil ||
		got.Role == nil || got.Role.Slug != "foundations" || got.Role.TicketsTotal != 2 || got.AllPassed {
		t.Fatalf("no progress: %+v", got)
	}

	setProgress(t, "foundations/b", "started", "running")
	got = today(t, tok)
	if got.Current == nil || got.Current.ID != "foundations/b" || *got.Current.State != "started" || *got.Current.LabState != "running" ||
		got.Next == nil || got.Next.ID != "foundations/a" || got.Role.Slug != "foundations" {
		t.Fatalf("started: %+v", got)
	}
	// Another learner's progress is not visible.
	if b := today(t, bobTok); b.Current != nil || b.Next.ID != "foundations/a" {
		t.Fatalf("bob sees ada's progress: %+v", b)
	}

	// Every ticket passed: foundations passes, then support-l1's only ticket.
	asOwner(t, "UPDATE ticket_progress SET state = 'passed', first_passed_at = now() WHERE item_id = 'foundations/b'")
	setProgress(t, "foundations/a", "passed", "stopped")
	got = today(t, tok)
	if got.Current != nil || got.Next == nil || got.Next.ID != "support-l1/c" || got.Role.Slug != "support-l1" || got.AllPassed {
		t.Fatalf("next role: %+v", got)
	}
	setProgress(t, "support-l1/c", "revisit", "")
	got = today(t, tok)
	if got.Current != nil || got.Next != nil || !got.AllPassed || got.Role == nil || got.Role.Slug != "support-l1" || got.Role.TicketsPassed != 1 {
		t.Fatalf("all passed: %+v", got)
	}
}

func TestTodayRefusesSuspendedIntegration(t *testing.T) {
	freshCatalog(t)
	seedCatalog(t)
	resetLearners(t, "1001")
	signUp(t, "ada")
	tok := cliToken(t, newCLIClient(t), "ada")
	if code, _, _ := getAs(t, "", "/v1/today"); code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", code)
	}
	asOwner(t, "UPDATE learners SET suspended_at = now() WHERE github_id = 1001")
	if code, body, _ := getAs(t, tok, "/v1/today"); code != http.StatusForbidden || body != "{\"error\":\"account_suspended\"}\n" {
		t.Fatalf("suspended: %d %q", code, body)
	}
}
