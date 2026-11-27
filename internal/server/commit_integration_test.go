//go:build integration

package server_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const (
	shaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	shaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func evCommit(id, sha string) map[string]any {
	return map[string]any{"id": id, "type": "ticket.commit", "at": "2026-11-23T10:20:00Z", "item_id": "ev-role/one", "commit": sha}
}

func evPass() map[string]any { return evCheck(evID(), "2026-11-23T10:10:00Z", "pass", "ok") }

const commitSQL = "SELECT coalesce(passed_commit, '-'), coalesce(commit_state, '-'), commit_tries, commit_at IS NOT NULL, " +
	"coalesce(round(extract(epoch FROM commit_next_at - commit_at))::text, '-') " +
	"FROM ticket_progress p JOIN learners l ON l.id = p.learner_id WHERE l.github_id = 1001 AND item_id = 'ev-role/one'"

const pendingSQL = "SELECT coalesce(string_agg(\"commit\", ','), '-') FROM pending_commits c JOIN learners l ON l.id = c.learner_id WHERE l.github_id = 1001"

func TestCommitAfterPassStartsPending(t *testing.T) {
	c, tok := eventsFixture(t)
	post(t, c, tok, evStarted(evID(), "ev-role/one", "2026-11-23T10:00:00Z"), evPass())
	if code, body := post(t, c, tok, evCommit(evID(), shaA)); code != 200 || strings.TrimSpace(body) != `{"accepted":1,"duplicates":0,"ignored":0}` {
		t.Fatalf("%d %q", code, body)
	}
	if got := asOwner(t, commitSQL); got != shaA+"|pending|0|t|5" {
		t.Fatalf("after commit: %q", got)
	}
	if got := asOwner(t, "SELECT payload::text FROM events WHERE type = 'ticket.commit'"); !strings.Contains(got, shaA) {
		t.Fatalf("event payload: %q", got)
	}
	if got := asOwner(t, pendingSQL); got != "-" {
		t.Fatalf("a commit after the pass must not wait: %q", got)
	}

	// First sha wins on pending and on verified; both events are stored.
	post(t, c, tok, evCommit(evID(), shaB))
	if got := asOwner(t, commitSQL); !strings.HasPrefix(got, shaA+"|pending|") {
		t.Fatalf("second sha on pending: %q", got)
	}
	asOwner(t, "UPDATE ticket_progress SET commit_state = 'verified', commit_tries = 3 WHERE learner_id IN (SELECT id FROM learners WHERE github_id = 1001)")
	post(t, c, tok, evCommit(evID(), shaB))
	if got := asOwner(t, commitSQL); !strings.HasPrefix(got, shaA+"|verified|3|") {
		t.Fatalf("second sha on verified: %q", got)
	}
	if n := asOwner(t, "SELECT count(*) FROM events WHERE type = 'ticket.commit'"); n != "3" {
		t.Fatalf("commit events stored: %q", n)
	}

	// An unverified ticket takes a new sha and starts over; the same sha changes nothing.
	asOwner(t, "UPDATE ticket_progress SET commit_state = 'unverified', commit_tries = 9 WHERE learner_id IN (SELECT id FROM learners WHERE github_id = 1001)")
	post(t, c, tok, evCommit(evID(), shaA))
	if got := asOwner(t, commitSQL); !strings.HasPrefix(got, shaA+"|unverified|9|") {
		t.Fatalf("same sha on unverified: %q", got)
	}
	post(t, c, tok, evCommit(evID(), shaB))
	if got := asOwner(t, commitSQL); got != shaB+"|pending|0|t|5" {
		t.Fatalf("recommit after unverified: %q", got)
	}
}

func TestCommitBeforePassWaitsAndIsApplied(t *testing.T) {
	c, tok := eventsFixture(t)
	// No row at all: the commit is kept, and no progress row is made for it.
	post(t, c, tok, evCommit(evID(), shaA))
	if got := asOwner(t, pendingSQL); got != shaA {
		t.Fatalf("pending: %q", got)
	}
	if n := asOwner(t, "SELECT count(*) FROM ticket_progress p JOIN learners l ON l.id = p.learner_id WHERE l.github_id = 1001"); n != "0" {
		t.Fatalf("progress rows made by a commit: %q", n)
	}
	// Started but not passed: first sha wins in pending_commits too.
	post(t, c, tok, evStarted(evID(), "ev-role/one", "2026-11-23T10:00:00Z"), evCommit(evID(), shaB))
	if got := asOwner(t, pendingSQL); got != shaA {
		t.Fatalf("pending after a second sha: %q", got)
	}
	if got := asOwner(t, commitSQL); got != "-|-|0|f|-" {
		t.Fatalf("started row: %q", got)
	}

	post(t, c, tok, evPass())
	if got := asOwner(t, commitSQL); got != shaA+"|pending|0|t|5" {
		t.Fatalf("after the pass: %q", got)
	}
	if got := asOwner(t, pendingSQL); got != "-" {
		t.Fatalf("the applied commit must be deleted: %q", got)
	}
}

func TestCommitAndPassInOneBatch(t *testing.T) {
	c, tok := eventsFixture(t)
	post(t, c, tok, evStarted(evID(), "ev-role/one", "2026-11-23T10:00:00Z"), evCommit(evID(), shaA), evPass())
	if got := asOwner(t, commitSQL); got != shaA+"|pending|0|t|5" {
		t.Fatalf("commit then pass: %q", got)
	}
	if got := asOwner(t, pendingSQL); got != "-" {
		t.Fatalf("pending left: %q", got)
	}
}

func TestCommitForUnknownTicketIsIgnored(t *testing.T) {
	c, tok := eventsFixture(t)
	ev := evCommit(evID(), shaA)
	ev["item_id"] = "gone-role/old"
	if code, body := post(t, c, tok, ev); code != 200 || strings.TrimSpace(body) != `{"accepted":1,"duplicates":0,"ignored":1}` {
		t.Fatalf("%d %q", code, body)
	}
	if got := asOwner(t, pendingSQL); got != "-" {
		t.Fatalf("pending: %q", got)
	}
}

func TestCommitStateIsShownToTheLearner(t *testing.T) {
	c, tok := eventsFixture(t)
	post(t, c, tok, evStarted(evID(), "ev-role/one", "2026-11-23T10:00:00Z"), evPass(), evCommit(evID(), shaA))
	progress := func() map[string]any {
		t.Helper()
		code, body := c.call(http.MethodGet, "/v1/tickets/ev-role/one", tok, nil)
		var got struct{ Progress map[string]any }
		if code != 200 || json.Unmarshal([]byte(body), &got) != nil {
			t.Fatalf("%d %q", code, body)
		}
		return got.Progress
	}
	recent := func() any {
		t.Helper()
		code, body := c.call(http.MethodGet, "/v1/activity", tok, nil)
		var got struct{ Recent []map[string]any }
		if code != 200 || json.Unmarshal([]byte(body), &got) != nil || len(got.Recent) != 1 {
			t.Fatalf("%d %q", code, body)
		}
		return got.Recent[0]["commit"]
	}
	if p := progress(); p["passed_commit"] != shaA || p["passed_commit_state"] != "pending" || p["passed_commit_url"] != nil || recent() != nil {
		t.Fatalf("pending: %v", p)
	}
	asOwner(t, "UPDATE ticket_progress SET commit_state = 'verified' WHERE learner_id IN (SELECT id FROM learners WHERE github_id = 1001)")
	if p := progress(); p["passed_commit_state"] != "verified" || p["passed_commit_url"] != "https://github.com/ada/dojo/commit/"+shaA || recent() != shaA {
		t.Fatalf("verified: %v", p)
	}
}

func TestPrunePendingCommitsKeepsThirtyDays(t *testing.T) {
	c, tok := eventsFixture(t)
	post(t, c, tok, evCommit(evID(), shaA))
	asOwner(t, "UPDATE pending_commits SET created_at = now() - interval '31 days'")
	if got := asOwner(t, "SELECT prune_pending_commits()"); got != "1" {
		t.Fatalf("old commit: %q", got)
	}
	post(t, c, tok, evCommit(evID(), shaB))
	asOwner(t, "UPDATE pending_commits SET created_at = now() - interval '29 days'")
	if got := asOwner(t, "SELECT prune_pending_commits()"); got != "0" {
		t.Fatalf("recent commit: %q", got)
	}
	for _, role := range []string{"dojo_api", "dojo_admin", "public"} {
		if got := asOwner(t, "SELECT has_function_privilege('"+role+"', 'prune_pending_commits()', 'EXECUTE')"); got != "f" {
			t.Fatalf("%s can execute prune_pending_commits(): %q", role, got)
		}
	}
}

func TestCommitConstraints(t *testing.T) {
	l := progressLearner(t)
	progressItem(t, "it/p")
	for name, c := range map[string]struct{ cols, vals string }{
		"bad commit_state":    {"state, commit_state", "'started', 'maybe'"},
		"negative tries":      {"state, commit_tries", "'started', -1"},
		"short passed_commit": {"state, passed_commit", "'started', 'abc'"},
		"upper-case sha":      {"state, passed_commit", "'started', repeat('A', 40)"},
	} {
		wantCheckViolation(t, name, tpInsert(l, c.cols, c.vals))
	}
	wantOK(t, "a full sha", tpInsert(l, "state, passed_commit, commit_state", "'started', repeat('a', 40), 'pending'"))
	wantCheckViolation(t, "pending sha short", "INSERT INTO pending_commits (org_id, learner_id, item_id, \"commit\", client_at) "+
		"SELECT org_id, id, 'it/p', 'abc', now() FROM learners WHERE id = '"+l+"'")
}
