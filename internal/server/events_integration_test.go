//go:build integration

package server_test

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

var evSeq atomic.Int64

// eventsFixture signs ada up, gives her a CLI token and one catalog ticket (ev-role/one).
func eventsFixture(t *testing.T) (c *cliClient, tok string) {
	t.Helper()
	clean := func() {
		asOwner(t, "DELETE FROM learners WHERE github_id = 1001")
		asOwner(t, "DELETE FROM catalog_items WHERE id LIKE 'ev-role/%'")
		asOwner(t, "DELETE FROM roles WHERE slug = 'ev-role'")
	}
	clean()
	t.Cleanup(clean)
	asOwner(t, "INSERT INTO roles (org_id, slug, title, kind, has_exam) SELECT id, 'ev-role', 'R', 'role', true FROM orgs WHERE slug = 'public'")
	asOwner(t, "INSERT INTO catalog_items (id, org_id, role_id, kind, position, title, est_minutes, catalog_version, brief, hints, bundle_key, bundle_sha256, bundle_bytes) "+
		"SELECT 'ev-role/one', o.id, r.id, 'ticket', 1, 'T', 5, '0123456789ab', 'brief', ARRAY['h'], 'bundles/ev-role/one/x.tar.gz', '"+ctSHA+"', 100 "+
		"FROM orgs o, roles r WHERE o.slug = 'public' AND r.slug = 'ev-role'")
	signUp(t, "ada")
	c = newCLIClient(t)
	return c, cliToken(t, c, "ada")
}

func evID() string { return fmt.Sprintf("0194f0f2-0000-7000-8000-%012d", evSeq.Add(1)) }

func evStarted(id, item, at string) map[string]any {
	return map[string]any{"id": id, "type": "ticket.started", "at": at, "item_id": item, "lab": "local", "catalog_version": "0123456789ab"}
}

func evCheck(id, at, result, output string) map[string]any {
	return map[string]any{"id": id, "type": "check.run", "at": at, "item_id": "ev-role/one", "result": result, "duration_ms": 10, "passed": 1, "total": 2, "output": output}
}

func post(t *testing.T, c *cliClient, tok string, evs ...map[string]any) (int, string) {
	t.Helper()
	return c.call(http.MethodPost, "/v1/events", tok, map[string]any{"events": evs})
}

const progressSQL = "SELECT state, lab_state, check_count, last_check_ok, coalesce(last_check_output, '-'), first_passed_at IS NOT NULL FROM ticket_progress p JOIN learners l ON l.id = p.learner_id WHERE l.github_id = 1001 AND item_id = 'ev-role/one'"

func TestEventsDuplicatesAndIgnored(t *testing.T) {
	c, tok := eventsFixture(t)
	a, b := evID(), evID()
	code, body := post(t, c, tok, evStarted(a, "ev-role/one", "2026-11-23T10:00:00Z"), evStarted(b, "gone-role/old", "2026-11-23T10:00:00Z"))
	if code != 200 || strings.TrimSpace(body) != `{"accepted":2,"duplicates":0,"ignored":1}` {
		t.Fatalf("first: %d %q", code, body)
	}
	if code, body = post(t, c, tok, evStarted(a, "ev-role/one", "2026-11-23T10:00:00Z"), evStarted(b, "gone-role/old", "2026-11-23T10:00:00Z")); code != 200 || strings.TrimSpace(body) != `{"accepted":0,"duplicates":2,"ignored":0}` {
		t.Fatalf("retry: %d %q", code, body)
	}
	if n := asOwner(t, "SELECT count(*) FROM events e JOIN learners l ON l.id = e.learner_id WHERE l.github_id = 1001"); n != "2" {
		t.Fatalf("events stored: %q", n)
	}
	if n := asOwner(t, "SELECT count(*) FROM ticket_progress p JOIN learners l ON l.id = p.learner_id WHERE l.github_id = 1001"); n != "1" {
		t.Fatalf("progress rows (the unknown item must project nothing): %q", n)
	}
	// A duplicate check never counts twice.
	chk := evCheck(evID(), "2026-11-23T10:05:00Z", "fail", "x")
	post(t, c, tok, chk)
	post(t, c, tok, chk)
	if got := asOwner(t, progressSQL); got != "started|starting|1|f|x|f" {
		t.Fatalf("progress: %q", got)
	}
}

func TestEventsBadIndexWritesNothing(t *testing.T) {
	c, tok := eventsFixture(t)
	bad := evCheck(evID(), "2026-11-23T10:05:00Z", "maybe", "")
	code, body := post(t, c, tok, evStarted(evID(), "ev-role/one", "2026-11-23T10:00:00Z"), bad)
	if code != 400 || strings.TrimSpace(body) != `{"error":"bad_request","index":1}` {
		t.Fatalf("got %d %q", code, body)
	}
	if n := asOwner(t, "SELECT count(*) FROM events e JOIN learners l ON l.id = e.learner_id WHERE l.github_id = 1001"); n != "0" {
		t.Fatalf("a refused batch wrote %q events", n)
	}
	if code, _ := c.call(http.MethodPost, "/v1/events", tok, map[string]any{"events": []any{}}); code != 400 {
		t.Fatalf("empty batch: %d", code)
	}
}

func TestEventsOutputOnlyInProgress(t *testing.T) {
	c, tok := eventsFixture(t)
	if code, body := post(t, c, tok, evCheck(evID(), "2026-11-23T10:05:00Z", "fail", "SECRET-OUTPUT")); code != 200 {
		t.Fatalf("%d %q", code, body)
	}
	if n := asOwner(t, "SELECT count(*) FROM events WHERE payload::text LIKE '%SECRET-OUTPUT%'"); n != "0" {
		t.Fatalf("output leaked into events: %q", n)
	}
	if got := asOwner(t, progressSQL); got != "started||1|f|SECRET-OUTPUT|f" {
		t.Fatalf("progress: %q", got)
	}
}

func TestEventsReplayedOlderEventsKeepNewerState(t *testing.T) {
	c, tok := eventsFixture(t)
	post(t, c, tok, evStarted(evID(), "ev-role/one", "2026-11-23T10:00:00Z"),
		evCheck(evID(), "2026-11-23T10:20:00Z", "fail", "newer"),
		map[string]any{"id": evID(), "type": "lab.stopped", "at": "2026-11-23T10:30:00Z", "item_id": "ev-role/one"})
	// Replayed from an offline queue: older than the recorded lab state and last check.
	post(t, c, tok,
		map[string]any{"id": evID(), "type": "lab.failed", "at": "2026-11-23T10:10:00Z", "item_id": "ev-role/one", "reason": "vm"},
		evCheck(evID(), "2026-11-23T10:10:00Z", "pass", "older"))
	// The older pass still passes the ticket (a pass applies whatever the order), but not the last check.
	got := asOwner(t, "SELECT state, lab_state, check_count, last_check_ok, last_check_output, first_passed_at IS NOT NULL FROM ticket_progress p JOIN learners l ON l.id = p.learner_id WHERE l.github_id = 1001")
	if got != "passed|stopped|2|f|newer|t" {
		t.Fatalf("progress: %q", got)
	}
}

func TestEventsPassThenRevisit(t *testing.T) {
	c, tok := eventsFixture(t)
	post(t, c, tok, evStarted(evID(), "ev-role/one", "2026-11-23T10:00:00Z"), evCheck(evID(), "2026-11-23T10:10:00Z", "pass", "ok"))
	if got := asOwner(t, progressSQL); got != "passed|starting|1|t|ok|t" {
		t.Fatalf("after pass: %q", got)
	}
	first := asOwner(t, "SELECT first_passed_at FROM ticket_progress p JOIN learners l ON l.id = p.learner_id WHERE l.github_id = 1001")

	// A start recorded before the pass (a replay) does not revisit. The pass time is the server's, so a
	// start dated after it is a real restart.
	post(t, c, tok, evStarted(evID(), "ev-role/one", "2026-11-23T10:05:00Z"))
	if got := asOwner(t, progressSQL); !strings.HasPrefix(got, "passed|") {
		t.Fatalf("older start: %q", got)
	}
	// The pass above carried a 2026-11 client time, but first_passed_at is now(); a client time after
	// it cannot be sent (it is clamped to now), so move the pass back to make a restart later than it.
	asOwner(t, "UPDATE ticket_progress SET first_passed_at = '2026-11-23T10:10:00Z' WHERE learner_id IN (SELECT id FROM learners WHERE github_id = 1001)")
	post(t, c, tok, evStarted(evID(), "ev-role/one", "2026-11-23T11:00:00Z"))
	if got := asOwner(t, progressSQL); !strings.HasPrefix(got, "revisit|starting|") {
		t.Fatalf("restart: %q", got)
	}
	// Passing again returns to passed and never moves first_passed_at.
	post(t, c, tok, evCheck(evID(), "2026-11-23T11:10:00Z", "pass", "again"))
	if got := asOwner(t, progressSQL); !strings.HasPrefix(got, "passed|") {
		t.Fatalf("pass in revisit: %q", got)
	}
	if now := asOwner(t, "SELECT first_passed_at FROM ticket_progress p JOIN learners l ON l.id = p.learner_id WHERE l.github_id = 1001"); now == first {
		t.Fatalf("test setup: first_passed_at was reset by hand and must differ from the original")
	} else if now != "2026-11-23 10:10:00+00" {
		t.Fatalf("first_passed_at moved: %q", now)
	}
}

func TestEventsRejectsCookiesAndHintOpened(t *testing.T) {
	c, tok := eventsFixture(t)
	if code, _ := c.call(http.MethodPost, "/v1/events", "", map[string]any{"events": []any{evStarted(evID(), "ev-role/one", "2026-11-23T10:00:00Z")}}); code != 401 {
		t.Fatalf("no credential: %d", code)
	}
	hint := map[string]any{"id": evID(), "type": "hint.opened", "at": "2026-11-23T10:00:00Z", "item_id": "ev-role/one"}
	if code, body := post(t, c, tok, hint); code != 400 || !strings.Contains(body, `"index":0`) {
		t.Fatalf("hint.opened: %d %q", code, body)
	}
}

func TestEventsNULInOutputIsStoredReplaced(t *testing.T) {
	c, tok := eventsFixture(t)
	if code, body := post(t, c, tok, evCheck(evID(), "2026-11-23T10:05:00Z", "fail", "a\x00b")); code != 200 {
		t.Fatalf("%d %q", code, body)
	}
	if got := asOwner(t, "SELECT last_check_output = E'a\\uFFFDb' FROM ticket_progress p JOIN learners l ON l.id = p.learner_id WHERE l.github_id = 1001"); got != "t" {
		t.Fatalf("output: %q", got)
	}
}

func TestEventsConcurrentBatchesOfOneLearner(t *testing.T) {
	c, tok := eventsFixture(t)
	post(t, c, tok, evStarted(evID(), "ev-role/one", "2026-11-23T10:00:00Z"))
	const workers, per = 8, 5
	var wg sync.WaitGroup
	codes := make(chan int, workers)
	for w := range workers {
		var evs []map[string]any
		for i := range per {
			evs = append(evs, evCheck(evID(), fmt.Sprintf("2026-11-23T10:%02d:%02dZ", w, i), "fail", "x"))
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, _ := post(t, c, tok, evs...)
			codes <- code
		}()
	}
	wg.Wait()
	close(codes)
	for code := range codes {
		if code != 200 {
			t.Fatalf("concurrent batch: %d", code)
		}
	}
	if got := asOwner(t, progressSQL); got != "started|starting|40|f|x|f" {
		t.Fatalf("progress: %q", got)
	}
}

func TestEventsNeverTouchAnotherLearnersProgress(t *testing.T) {
	c, tokAda := eventsFixture(t)
	asOwner(t, "DELETE FROM learners WHERE github_id = 1002")
	t.Cleanup(func() { asOwner(t, "DELETE FROM learners WHERE github_id = 1002") })
	signUp(t, "bob")
	tokBob := cliToken(t, c, "bob")
	post(t, c, tokAda, evStarted(evID(), "ev-role/one", "2026-11-23T10:00:00Z"), evCheck(evID(), "2026-11-23T10:05:00Z", "pass", "ada"))
	post(t, c, tokBob, evCheck(evID(), "2026-11-23T10:06:00Z", "fail", "bob"))
	q := "SELECT state, check_count, last_check_output FROM ticket_progress p JOIN learners l ON l.id = p.learner_id WHERE l.github_id = "
	if got := asOwner(t, q+"1001"); got != "passed|1|ada" {
		t.Fatalf("ada: %q", got)
	}
	if got := asOwner(t, q+"1002"); got != "started|1|bob" {
		t.Fatalf("bob: %q", got)
	}
}
