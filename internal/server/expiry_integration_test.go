//go:build integration

package server_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kibanyadev/dojo/internal/server/repo"
)

func expiryStore(t *testing.T) *repo.Store {
	t.Helper()
	s, err := repo.Open(context.Background(), apiDBURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

// expiryRow adds a catalog ticket ev-role/<name> and ada's progress row on it, last seen ageDays ago.
func expiryRow(t *testing.T, name, labState string, ageDays int) string {
	t.Helper()
	id := "ev-role/" + name
	asOwner(t, "INSERT INTO catalog_items (id, org_id, role_id, kind, position, title, est_minutes, catalog_version, brief, hints, bundle_key, bundle_sha256, bundle_bytes) "+
		"SELECT '"+id+"', o.id, r.id, 'ticket', (SELECT coalesce(max(position), 0) + 1 FROM catalog_items WHERE role_id = r.id), 'T', 5, '0123456789ab', 'brief', ARRAY['h'], 'bundles/"+id+"/x.tar.gz', '"+ctSHA+"', 100 "+
		"FROM orgs o, roles r WHERE o.slug = 'public' AND r.slug = 'ev-role'")
	asOwner(t, fmt.Sprintf("INSERT INTO ticket_progress (org_id, learner_id, item_id, state, lab, lab_state, lab_state_at, first_started_at, last_seen_at) "+
		"SELECT org_id, id, '%s', 'started', 'hosted', '%s', now() - interval '%d days', now() - interval '%d days', now() - interval '%d days' FROM learners WHERE github_id = 1001",
		id, labState, ageDays, ageDays, ageDays))
	return id
}

func labState(t *testing.T, id string) string {
	t.Helper()
	return asOwner(t, "SELECT lab_state FROM ticket_progress p JOIN learners l ON l.id = p.learner_id WHERE l.github_id = 1001 AND item_id = '"+id+"'")
}

func TestExpirySweepExpiresOnlyStaleLiveLabsAndNotifies(t *testing.T) {
	_, _, ada := streamFixture(t)
	old := expiryRow(t, "old", "running", 8)
	starting := expiryRow(t, "starting", "starting", 8)
	failed := expiryRow(t, "failed", "failed", 30)
	stopped := expiryRow(t, "stopped", "stopped", 30)
	recent := expiryRow(t, "recent", "running", 6)
	s := openStream(t, apiURL(t), cookieOf(t, ada))
	s.next("ready", 5*time.Second)

	n, err := expiryStore(t).ExpireLabs(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("expired %d, %v", n, err)
	}
	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		ev := s.next("progress", 5*time.Second)
		if !strings.Contains(ev.data, `"lab_state":"expired"`) || strings.Contains(ev.data, "SECRET") {
			t.Fatalf("progress: %s", ev.data)
		}
		for _, id := range []string{old, starting} {
			if strings.Contains(ev.data, `"item_id":"`+id+`"`) {
				seen[id] = true
			}
		}
	}
	if len(seen) != 2 {
		t.Fatalf("events for %v", seen)
	}
	s.quiet(time.Second)
	for id, want := range map[string]string{old: "expired", starting: "expired", failed: "failed", stopped: "stopped", recent: "running"} {
		if got := labState(t, id); got != want {
			t.Fatalf("%s: lab_state %q, want %q", id, got, want)
		}
	}
	if got := asOwner(t, "SELECT lab_state_at = last_seen_at + interval '7 days' AND updated_at > now() - interval '1 minute' AND last_seen_at < now() - interval '7 days' FROM ticket_progress WHERE item_id = '"+old+"'"); got != "t" {
		t.Fatalf("timestamps: %q", got)
	}
}

func TestExpiryTwoSweepersDoNotDoubleProcess(t *testing.T) {
	_, _, ada := streamFixture(t)
	const rows = 6
	for i := 0; i < rows; i++ {
		expiryRow(t, fmt.Sprintf("p%d", i), "running", 9)
	}
	s := openStream(t, apiURL(t), cookieOf(t, ada))
	s.next("ready", 5*time.Second)
	a, b := expiryStore(t), expiryStore(t)

	var wg sync.WaitGroup
	counts := make([]int, 2)
	for i, st := range []*repo.Store{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := st.ExpireLabs(context.Background())
			if err != nil {
				t.Errorf("sweeper %d: %v", i, err)
			}
			counts[i] = n
		}()
	}
	wg.Wait()
	if counts[0]+counts[1] != rows {
		t.Fatalf("sweepers expired %v, want %d in total", counts, rows)
	}
	for i := 0; i < rows; i++ {
		s.next("progress", 5*time.Second)
	}
	s.quiet(time.Second)
}

func TestExpiryThenTicketStartedLeavesExpired(t *testing.T) {
	// The row was last seen 9 days ago, so it expired 2 days ago: a start 1 day ago, one a minute
	// ago and one now all come after the expiry instant.
	for name, ago := range map[string]string{"between expiry and sweep": "1 day", "a minute before the sweep": "1 minute", "now": "0 seconds"} {
		t.Run(name, func(t *testing.T) {
			c, tok, _ := streamFixture(t)
			id := expiryRow(t, "back", "running", 9)
			if n, err := expiryStore(t).ExpireLabs(context.Background()); err != nil || n != 1 {
				t.Fatalf("expired %d, %v", n, err)
			}
			if got := labState(t, id); got != "expired" {
				t.Fatalf("after sweep: %q", got)
			}
			at := strings.TrimSpace(asOwner(t, "SELECT to_char((now() - interval '"+ago+"') AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"')"))
			if code, body := post(t, c, tok, evStarted(evID(), id, at)); code != 200 {
				t.Fatalf("%d %q", code, body)
			}
			if got := labState(t, id); got != "starting" {
				t.Fatalf("after ticket.started at %s: %q", at, got)
			}
		})
	}
}
