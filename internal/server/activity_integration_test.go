//go:build integration

package server_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

type activityBody struct {
	From, To string
	Days     []struct {
		Day   string
		Count int
	}
	Total  int
	Recent []struct {
		TicketID string `json:"ticket_id"`
		Title    string
		At       string
		Commit   *string
	}
}

func activity(t *testing.T, tok, query string) activityBody {
	t.Helper()
	code, body, h := getAs(t, tok, "/v1/activity"+query)
	var got activityBody
	if err := json.Unmarshal([]byte(body), &got); err != nil || code != 200 || h.Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %v %q %s", code, err, h.Get("Cache-Control"), body)
	}
	return got
}

// pass records a first pass for a learner at an instant given as an SQL expression.
func pass(t *testing.T, githubID, item, state, at string) {
	t.Helper()
	asOwner(t, "INSERT INTO ticket_progress (org_id, learner_id, item_id, state, first_started_at, first_passed_at) "+
		"SELECT org_id, id, '"+item+"', '"+state+"', "+at+" - interval '1 hour', "+at+" FROM learners WHERE github_id = "+githubID)
}

func TestActivityIntegration(t *testing.T) {
	freshCatalog(t)
	seedCatalog(t)
	resetLearners(t, "1001", "1002")
	signUp(t, "ada")
	signUp(t, "bob")
	tok := cliToken(t, newCLIClient(t), "ada")
	bobTok := cliToken(t, newCLIClient(t), "bob")

	// A learner with no passes gets empty lists, not null.
	code, body, _ := getAs(t, tok, "/v1/activity")
	if code != 200 || !strings.Contains(body, `"days":[],"total":0,"recent":[]`) {
		t.Fatalf("empty: %d %s", code, body)
	}

	// Yesterday 23:30 and 23:45 UTC, and one pass 141 days ago (outside the window, inside the total).
	yday := "(date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' - interval '1 day' + interval '23 hours 30 minutes')"
	pass(t, "1001", "foundations/a", "passed", yday)
	pass(t, "1001", "foundations/b", "passed", yday+" + interval '15 minutes'")
	pass(t, "1001", "support-l1/c", "passed", "now() - interval '141 days'")
	pass(t, "1001", "foundations/q", "passed", "now()") // a quiz is not a ticket pass
	pass(t, "1002", "foundations/a", "passed", "now()")

	utcDay := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	utcToday := time.Now().UTC().Format("2006-01-02")
	got := activity(t, tok, "")
	if len(got.Days) != 1 || got.Days[0].Day != utcDay || got.Days[0].Count != 2 || got.Total != 3 || got.To != utcToday {
		t.Fatalf("utc: %+v", got)
	}
	if len(got.Recent) != 3 || got.Recent[0].TicketID != "foundations/b" || got.Recent[0].Title != "Free the disk" ||
		got.Recent[1].TicketID != "foundations/a" || got.Recent[2].TicketID != "support-l1/c" || got.Recent[0].Commit != nil {
		t.Fatalf("recent: %+v", got.Recent)
	}
	if _, err := time.Parse(time.RFC3339, got.Recent[0].At); err != nil {
		t.Fatalf("at: %v", err)
	}

	// 23:30 UTC is already the next day in Auckland (UTC+12 or +13) and the same day in Los Angeles (UTC-7 or -8).
	if auck := activity(t, tok, "?tz=Pacific/Auckland"); len(auck.Days) != 1 || auck.Days[0].Day != time.Now().UTC().Format("2006-01-02") ||
		auck.Days[0].Count != 2 || auck.Total != 3 {
		t.Fatalf("auckland: %+v", auck)
	}
	if la := activity(t, tok, "?tz=America/Los_Angeles"); len(la.Days) != 1 || la.Days[0].Day != utcDay || la.Days[0].Count != 2 {
		t.Fatalf("los angeles: %+v", la)
	}

	// A revisit changes the state and last_seen_at, never first_passed_at: still counted once.
	asOwner(t, "UPDATE ticket_progress SET state = 'revisit', last_seen_at = now(), updated_at = now() WHERE item_id = 'foundations/a' AND learner_id = (SELECT id FROM learners WHERE github_id = 1001)")
	if again := activity(t, tok, ""); len(again.Days) != 1 || again.Days[0].Count != 2 || again.Total != 3 {
		t.Fatalf("revisit: %+v", again)
	}

	// Bob sees only his own pass.
	if b := activity(t, bobTok, ""); len(b.Days) != 1 || b.Days[0].Count != 1 || b.Total != 1 || len(b.Recent) != 1 || b.Recent[0].TicketID != "foundations/a" {
		t.Fatalf("bob: %+v", b)
	}

	if code, body, _ := getAs(t, tok, "/v1/activity?tz=Mars/Base"); code != http.StatusBadRequest || body != "{\"error\":\"bad_request\"}\n" {
		t.Fatalf("bad tz: %d %q", code, body)
	}
}

func TestActivityRefusesSuspendedIntegration(t *testing.T) {
	freshCatalog(t)
	seedCatalog(t)
	resetLearners(t, "1001")
	signUp(t, "ada")
	tok := cliToken(t, newCLIClient(t), "ada")
	if code, _, _ := getAs(t, "", "/v1/activity"); code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", code)
	}
	asOwner(t, "UPDATE learners SET suspended_at = now() WHERE github_id = 1001")
	if code, body, _ := getAs(t, tok, "/v1/activity"); code != http.StatusForbidden || body != "{\"error\":\"account_suspended\"}\n" {
		t.Fatalf("suspended: %d %q", code, body)
	}
}

// The three activity reads must stay on the ticket_progress primary key (learner_id, item_id) at a
// realistic size: 1,000 learners with 200 passes each. No extra index is wanted.
func TestActivityQueriesUseThePrimaryKey(t *testing.T) {
	freshCatalog(t)
	seedCatalog(t)
	resetLearners(t, "1001")
	signUp(t, "ada")
	org := "(SELECT id FROM orgs WHERE slug = 'public')"
	asOwner(t, "INSERT INTO catalog_items (id, org_id, role_id, kind, position, title, est_minutes, tags, brief, hints, bundle_key, bundle_sha256, bundle_bytes, catalog_version) "+
		"SELECT 'foundations/bulk-' || n, "+org+", r.id, 'ticket', 100 + n, 'Bulk ' || n, 10, '{}', 'b', ARRAY['h'], 'bundles/x.tar.gz', repeat('a', 64), 100, 'a1b2c3d4e5f6' "+
		"FROM roles r, generate_series(1, 200) n WHERE r.slug = 'foundations'")
	asOwner(t, "INSERT INTO learners (org_id, github_id, github_login, username, email, language, age_confirmed_at, terms_accepted_at, terms_version) "+
		"SELECT o.id, 950000000 + n, 'Bulk-Login-' || n, 'bulk-' || n, 'bulk' || n || '@example.com', 'en', now(), now(), '2026-11-20' "+
		"FROM orgs o, generate_series(1, 1000) n WHERE o.slug = 'public'")
	t.Cleanup(func() {
		asOwner(t, "DELETE FROM learners WHERE github_id BETWEEN 950000000 AND 950001000")
		asOwner(t, "ANALYZE ticket_progress")
	})
	asOwner(t, "INSERT INTO ticket_progress (org_id, learner_id, item_id, state, first_started_at, first_passed_at) "+
		"SELECT l.org_id, l.id, i.id, 'passed', now() - interval '1 day', now() - random() * interval '300 days' "+
		"FROM learners l JOIN catalog_items i ON i.id LIKE 'foundations/bulk-%' WHERE l.github_id >= 950000000 OR l.github_id = 1001")
	asOwner(t, "ANALYZE ticket_progress")
	lid := "(SELECT id FROM learners WHERE github_id = 1001)"
	for _, q := range []string{
		"SELECT (p.first_passed_at AT TIME ZONE 'UTC')::date, count(*) FROM ticket_progress p JOIN catalog_items i ON i.id = p.item_id AND i.kind = 'ticket' WHERE p.learner_id = " + lid + " AND p.first_passed_at >= now() - interval '140 days' GROUP BY 1 ORDER BY 1",
		"SELECT count(*) FROM ticket_progress p JOIN catalog_items i ON i.id = p.item_id AND i.kind = 'ticket' WHERE p.learner_id = " + lid + " AND p.first_passed_at IS NOT NULL",
		"SELECT p.item_id, i.title FROM ticket_progress p JOIN catalog_items i ON i.id = p.item_id AND i.kind = 'ticket' WHERE p.learner_id = " + lid + " AND p.first_passed_at IS NOT NULL ORDER BY p.first_passed_at DESC, p.item_id LIMIT 3",
	} {
		plan := asOwner(t, "EXPLAIN "+q)
		if !strings.Contains(plan, "ticket_progress_pkey") || strings.Contains(plan, "Seq Scan on ticket_progress") {
			t.Errorf("plan leaves the primary key:\n%s", plan)
		} else {
			t.Logf("uses ticket_progress_pkey:\n%s", plan)
		}
	}
}
