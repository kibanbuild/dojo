//go:build integration

package admin_test

import (
	"context"
	"encoding/hex"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kibanbuild/dojo/internal/admin"
	"github.com/kibanbuild/dojo/internal/server"
)

var (
	itCookieToken = "zzitcookie" + strings.Repeat("0", 33)        // 43 characters
	itCLIToken    = "dojo_zzitclitoken" + strings.Repeat("0", 31) // dojo_ and 43 characters
)

func tokenHash(tok string) string { return `\x` + hex.EncodeToString(server.HashToken(tok)) }

// seedAPICredentials gives the first seeded learner a web session and a CLI token.
func seedAPICredentials(t *testing.T) {
	t.Helper()
	psqlAsOwner(t, "INSERT INTO sessions (org_id, learner_id, token_hash) SELECT org_id, id, '"+tokenHash(itCookieToken)+"' FROM learners WHERE github_id = 940000001")
	psqlAsOwner(t, "INSERT INTO cli_tokens (org_id, learner_id, token_hash, device_name, os, cli_version) SELECT org_id, id, '"+tokenHash(itCLIToken)+"', 'it', 'linux', '1' FROM learners WHERE github_id = 940000001")
}

// apiAnswers returns what the real API says to the seeded cookie session and CLI token.
func apiAnswers(t *testing.T) (cookie, bearer int) {
	t.Helper()
	get := func(base, path string, set func(*http.Request)) int {
		req, err := http.NewRequest(http.MethodGet, base+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		set(req)
		resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	cookie = get(os.Getenv("DOJO_EDGE_URL"), "/v1/me", func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: server.SessionCookie, Value: itCookieToken})
	})
	bearer = get(os.Getenv("DOJO_API_URL"), "/v1/catalog", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+itCLIToken)
	})
	return cookie, bearer
}

func TestSuspendAndUnsuspendEndToEnd(t *testing.T) {
	id1, _ := seedLearners(t)
	seedAPICredentials(t)
	b := signedInBrowser(t)
	if c, br := apiAnswers(t); c != http.StatusOK || br != http.StatusOK {
		t.Fatalf("before: cookie %d, bearer %d", c, br)
	}

	_, body := b.admin(http.MethodGet, "/learners/"+id1+"/suspend")
	if !strings.Contains(body, "What will change") || !strings.Contains(body, "required") {
		t.Fatalf("confirm page: %s", body)
	}
	if n := psqlAsOwner(t, "SELECT count(*) FROM admin_audit WHERE action LIKE 'learner.%suspend'"); n != "0" {
		t.Fatalf("the confirm page wrote %s audit rows", n)
	}

	resp := b.postForm("/learners/"+id1+"/suspend", url.Values{"csrf_token": {b.csrf()}, "reason": {"abusive name"}}, nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/learners/"+id1 {
		t.Fatalf("suspend: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if got := psqlAsOwner(t, "SELECT suspended_at IS NOT NULL FROM learners WHERE id = '"+id1+"'"); got != "t" {
		t.Fatalf("suspended_at set = %s", got)
	}
	if got := psqlAsOwner(t, "SELECT operator_login || '|' || target_type || '|' || target_id || '|' || reason || '|' || (before->'suspended_at')::text || '|' || (after->>'suspended_at' = (SELECT to_char(suspended_at AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"') FROM learners WHERE id = '"+id1+"'))::text "+
		"FROM admin_audit WHERE action = 'learner.suspend'"); !strings.HasPrefix(got, "ada|learner|"+id1+"|abusive name|null|") {
		t.Fatalf("suspend audit row = %q", got)
	}
	if c, br := apiAnswers(t); c != http.StatusForbidden || br != http.StatusForbidden {
		t.Fatalf("after suspend: cookie %d, bearer %d, want 403 both", c, br)
	}

	_, body = b.admin(http.MethodGet, "/learners/"+id1)
	for _, want := range []string{"Suspended since", "Operator history", "abusive name", "Suspended</td>", "/unsuspend"} {
		if !strings.Contains(body, want) {
			t.Errorf("learner page lacks %q", want)
		}
	}

	resp = b.postForm("/learners/"+id1+"/unsuspend", url.Values{"csrf_token": {b.csrf()}, "reason": {"appeal accepted"}}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("unsuspend: %d", resp.StatusCode)
	}
	if got := psqlAsOwner(t, "SELECT suspended_at IS NULL FROM learners WHERE id = '"+id1+"'"); got != "t" {
		t.Fatalf("suspended_at cleared = %s", got)
	}
	if got := psqlAsOwner(t, "SELECT reason || '|' || (before->>'suspended_at' IS NOT NULL)::text || '|' || (after->'suspended_at')::text FROM admin_audit WHERE action = 'learner.unsuspend'"); got != "appeal accepted|true|null" {
		t.Fatalf("unsuspend audit row = %q", got)
	}
	if c, br := apiAnswers(t); c != http.StatusOK || br != http.StatusOK {
		t.Fatalf("after unsuspend: cookie %d, bearer %d", c, br)
	}

	_, body = b.admin(http.MethodGet, "/learners/"+id1)
	if strings.Index(body, "Unsuspended</td>") < 0 || strings.Index(body, "Unsuspended</td>") > strings.Index(body, "Suspended</td>") {
		t.Fatalf("history order: %s", body)
	}
	if got := psqlAsOwner(t, "SELECT count(*) FROM admin_audit WHERE operator_login = 'ada' AND (row_to_json(admin_audit)::text LIKE '%@%' OR row_to_json(admin_audit)::text ILIKE '%zz-it-%')"); got != "0" {
		t.Fatalf("personal data reached %s audit rows", got)
	}
}

func TestSuspendRefusalsWriteNothing(t *testing.T) {
	id1, id2 := seedLearners(t)
	b := signedInBrowser(t)
	tok := b.csrf()
	form := func(csrf, reason string) url.Values { return url.Values{"csrf_token": {csrf}, "reason": {reason}} }

	cases := []struct {
		name string
		path string
		form url.Values
		hdr  map[string]string
		want int
	}{
		{"no reason", "/learners/" + id1 + "/suspend", form(tok, ""), nil, http.StatusBadRequest},
		{"blank reason", "/learners/" + id1 + "/suspend", form(tok, "   "), nil, http.StatusBadRequest},
		{"reason too long", "/learners/" + id1 + "/suspend", form(tok, strings.Repeat("r", 1001)), nil, http.StatusBadRequest},
		{"no csrf", "/learners/" + id1 + "/suspend", form("", "x"), nil, http.StatusForbidden},
		{"bad csrf", "/learners/" + id1 + "/suspend", form("nope", "x"), nil, http.StatusForbidden},
		{"cross-site", "/learners/" + id1 + "/suspend", form(tok, "x"), map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"suspend a suspended learner", "/learners/" + id2 + "/suspend", form(tok, "x"), nil, http.StatusConflict},
		{"unsuspend an active learner", "/learners/" + id1 + "/unsuspend", form(tok, "x"), nil, http.StatusConflict},
		{"unknown learner", "/learners/0192b7c8-0000-7000-8000-0000000000ff/suspend", form(tok, "x"), nil, http.StatusNotFound},
		{"not a uuid", "/learners/zz-it-one/suspend", form(tok, "x"), nil, http.StatusNotFound},
	}
	for _, c := range cases {
		if resp := b.postForm(c.path, c.form, c.hdr); resp.StatusCode != c.want {
			t.Errorf("%s: %d, want %d", c.name, resp.StatusCode, c.want)
		}
	}
	if n := psqlAsOwner(t, "SELECT count(*) FROM admin_audit WHERE action IN ('learner.suspend', 'learner.unsuspend')"); n != "0" {
		t.Fatalf("refused requests wrote %s audit rows", n)
	}
	if got := psqlAsOwner(t, "SELECT (SELECT suspended_at IS NULL FROM learners WHERE id = '"+id1+"') AND (SELECT suspended_at IS NOT NULL FROM learners WHERE id = '"+id2+"')"); got != "t" {
		t.Fatal("a refused request changed a learner")
	}
}

func TestSuspendNeedsAnOperatorSession(t *testing.T) {
	id1, _ := seedLearners(t)
	seedOperator(t, "1001", "ada", false)
	b := newOperatorBrowser(t)
	if resp, _ := b.admin(http.MethodGet, "/learners/"+id1+"/suspend"); resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/signin" {
		t.Fatalf("GET: %d", resp.StatusCode)
	}
	resp := b.postForm("/learners/"+id1+"/suspend", url.Values{"reason": {"x"}}, nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/signin" {
		t.Fatalf("POST: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if got := psqlAsOwner(t, "SELECT suspended_at IS NULL FROM learners WHERE id = '"+id1+"'"); got != "t" {
		t.Fatal("a non-operator suspended a learner")
	}
}

func TestAFailedAuditRollsTheSuspensionBack(t *testing.T) {
	id1, _ := seedLearners(t)
	store := admin.PGLearners{Pool: adminPool(t)}
	ghost := admin.Operator{ID: "0192b7c8-0000-7000-8000-0000000000ee", Login: "ghost"} // not in operators: the audit FK fails
	if err := store.SetSuspended(context.Background(), ghost, id1, true, "x"); err == nil {
		t.Fatal("suspend with an unwritable audit row succeeded")
	}
	if got := psqlAsOwner(t, "SELECT suspended_at IS NULL FROM learners WHERE id = '"+id1+"'"); got != "t" {
		t.Fatal("the suspension survived a failed audit")
	}
}

func TestOperatorHistoryIsCappedAndNewestFirst(t *testing.T) {
	id1, _ := seedLearners(t)
	b := signedInBrowser(t)
	for i := 1; i <= 25; i++ {
		psqlAsOwner(t, "INSERT INTO admin_audit (operator_id, operator_login, action, target_type, target_id, reason, at) "+
			"SELECT id, 'ada', 'learner.view', 'learner', '"+id1+"', 'note "+pad(i)+"', now() - interval '"+pad(30-i)+" minutes' FROM operators WHERE github_id = 1001")
	}
	_, body := b.admin(http.MethodGet, "/learners/"+id1)
	if n := strings.Count(body, "<td>Viewed</td>"); n != 20 {
		t.Fatalf("history rows = %d, want 20", n)
	}
	if !strings.Contains(body, "note 25") || strings.Contains(body, "note 05") || strings.Index(body, "note 25") > strings.Index(body, "note 24") {
		t.Fatalf("history is not the newest 20, newest first: %s", body)
	}
}

func pad(i int) string {
	if i < 10 {
		return "0" + string(rune('0'+i))
	}
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}

// The 00014 and 00015 indexes must serve the lookups at a realistic size: 20,000 filler learners and
// 20,000 filler audit rows, analyzed, and the planner left alone.
func TestLookupQueriesUseTheirIndexes(t *testing.T) {
	seedLearners(t)
	clean := func() {
		psqlAsOwner(t, "DELETE FROM learners WHERE github_id BETWEEN 950000000 AND 950020000")
		psqlAsOwner(t, "DELETE FROM admin_audit WHERE target_id LIKE 'zz-it-bulk-%'")
	}
	clean()
	t.Cleanup(func() { clean(); psqlAsOwner(t, "ANALYZE learners; ANALYZE admin_audit") })
	psqlAsOwner(t, "INSERT INTO learners (org_id, github_id, github_login, username, email, language, age_confirmed_at, terms_accepted_at, terms_version) "+
		"SELECT o.id, 950000000 + n, 'Bulk-Login-' || n, 'bulk-' || n, 'bulk' || n || '@example.com', 'en', now(), now(), '2026-11-20' "+
		"FROM orgs o, generate_series(1, 20000) n WHERE o.slug = 'public'")
	seedOperator(t, "1001", "ada", false)
	psqlAsOwner(t, "INSERT INTO admin_audit (operator_id, operator_login, action, target_type, target_id) "+
		"SELECT op.id, 'ada', 'learner.view', 'learner', 'zz-it-bulk-' || (n % 2000) FROM operators op, generate_series(1, 20000) n WHERE op.github_id = 1001")
	psqlAsOwner(t, "ANALYZE learners; ANALYZE admin_audit")
	plan := func(q string) string {
		return psqlAsOwner(t, "EXPLAIN "+q)
	}
	for _, c := range []struct{ name, query, index string }{
		{"username prefix", "SELECT id FROM learners WHERE username LIKE 'zz-it%' ESCAPE '\\' ORDER BY username LIMIT 26", "learners_username_prefix_idx"},
		{"github login prefix", "SELECT id FROM learners WHERE lower(github_login) LIKE 'zz-it%' ESCAPE '\\' ORDER BY username LIMIT 26", "learners_github_login_prefix_idx"},
		{"exact email", "SELECT id FROM learners WHERE lower(email) = 'zz.it.one@example.com' ORDER BY username LIMIT 26", "learners_email_lower_idx"},
		{"operator history", "SELECT at FROM admin_audit WHERE target_type = 'learner' AND target_id = 'zz-it-bulk-5' ORDER BY at DESC, id DESC LIMIT 20", "admin_audit_target_idx"},
	} {
		if p := plan(c.query); !strings.Contains(p, c.index) {
			t.Errorf("%s does not use %s:\n%s", c.name, c.index, p)
		} else {
			t.Logf("%s uses %s", c.name, c.index)
		}
	}
}
