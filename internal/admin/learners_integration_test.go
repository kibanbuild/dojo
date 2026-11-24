//go:build integration

package admin_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

const (
	lookupMail1 = "zz.it.one@example.com"
	lookupMail2 = "zz.it.two@example.com"
)

// seedLearners inserts two learners, a role with one ticket and one passed row, and removes them all.
func seedLearners(t *testing.T) (id1, id2 string) {
	t.Helper()
	clean := func() {
		psqlAsOwner(t, "DELETE FROM ticket_progress WHERE item_id = 'zz-it-role/t-one'")
		psqlAsOwner(t, "DELETE FROM catalog_items WHERE id = 'zz-it-role/t-one'")
		psqlAsOwner(t, "DELETE FROM roles WHERE slug = 'zz-it-role'")
		psqlAsOwner(t, "DELETE FROM learners WHERE github_id IN (940000001, 940000002)")
	}
	clean()
	t.Cleanup(clean)
	ins := func(ghID, login, user, mail, extra string) string {
		out := psqlAsOwner(t, "INSERT INTO learners (org_id, github_id, github_login, username, email, language, age_confirmed_at, terms_accepted_at, terms_version"+extra+") "+
			"SELECT id, "+ghID+", '"+login+"', '"+user+"', '"+mail+"', 'es', now(), now(), '2026-11-20'"+map[bool]string{true: ", now()", false: ""}[extra != ""]+
			" FROM orgs WHERE slug = 'public' RETURNING id")
		return strings.Fields(out)[0]
	}
	id1 = ins("940000001", "ZZ-It-Login", "zz-it-one", lookupMail1, "")
	id2 = ins("940000002", "zz-it-two-login", "zz-it-two", lookupMail2, ", suspended_at")
	psqlAsOwner(t, "INSERT INTO roles (org_id, slug, title, kind, has_exam) SELECT id, 'zz-it-role', 'ZZ IT Role', 'role', false FROM orgs WHERE slug = 'public'")
	psqlAsOwner(t, "INSERT INTO catalog_items (id, org_id, role_id, kind, position, title, est_minutes, brief, hints, catalog_version, bundle_key, bundle_sha256, bundle_bytes) "+
		"SELECT 'zz-it-role/t-one', o.id, r.id, 'ticket', 1, 'IT ticket', 5, 'b', ARRAY['h'], '0123456789ab', 'k', repeat('a', 64), 10 FROM orgs o, roles r WHERE o.slug = 'public' AND r.slug = 'zz-it-role'")
	psqlAsOwner(t, "INSERT INTO ticket_progress (org_id, learner_id, item_id, state, first_started_at, first_passed_at, check_count, last_check_passed, last_check_total) "+
		"SELECT org_id, id, 'zz-it-role/t-one', 'passed', now(), now(), 2, 3, 3 FROM learners WHERE github_id = 940000001")
	return id1, id2
}

func signedInBrowser(t *testing.T) *operatorBrowser {
	t.Helper()
	seedOperator(t, "1001", "ada", false)
	b := newOperatorBrowser(t)
	if sessionCookie(b.signIn("ada")) == nil {
		t.Fatal("no session")
	}
	return b
}

func searchPath(field, q, reason string) string {
	v := url.Values{"field": {field}, "q": {q}}
	if reason != "" {
		v.Set("reason", reason)
	}
	return "/learners?" + v.Encode()
}

func TestLearnerLookupNeedsAnOperatorSession(t *testing.T) {
	seedOperator(t, "1001", "ada", false)
	b := newOperatorBrowser(t)
	for _, p := range []string{"/learners", searchPath("username", "zz-it", ""), "/learners/0192b7c8-0000-7000-8000-000000000001"} {
		resp, _ := b.admin(http.MethodGet, p)
		if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/signin" {
			t.Errorf("%s: %d %q", p, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	if n := psqlAsOwner(t, "SELECT count(*) FROM admin_audit WHERE operator_login = 'ada'"); n != "0" {
		t.Fatalf("a refused request wrote %s audit rows", n)
	}
}

func TestLearnerLookupRefusesARevokedOperator(t *testing.T) {
	id1, _ := seedLearners(t)
	b := signedInBrowser(t)
	psqlAsOwner(t, "UPDATE operators SET revoked_at = now() WHERE github_id = 1001")
	for _, p := range []string{searchPath("username", "zz-it", ""), "/learners/" + id1} {
		resp, _ := b.admin(http.MethodGet, p)
		if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/signin" {
			t.Errorf("%s: %d %q", p, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	if n := psqlAsOwner(t, "SELECT count(*) FROM admin_audit WHERE operator_login = 'ada'"); n != "0" {
		t.Fatalf("a refused request wrote %s audit rows", n)
	}
}

func TestSearchByPrefixAuditsTheFieldNeverTheQuery(t *testing.T) {
	id1, id2 := seedLearners(t)
	b := signedInBrowser(t)

	resp, body := b.admin(http.MethodGet, searchPath("username", "ZZ-IT-", "ticket 12"))
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "zz-it-one") || !strings.Contains(body, "zz-it-two") ||
		!strings.Contains(body, "/learners/"+id1) || !strings.Contains(body, "/learners/"+id2) {
		t.Fatalf("username prefix: %d %s", resp.StatusCode, body)
	}
	if got := psqlAsOwner(t, "SELECT action || '|' || target_type || '|' || target_id || '|' || coalesce(reason, '') || '|' || after::text || '|' || operator_login "+
		"FROM admin_audit WHERE action = 'learner.search'"); got != `learner.search|learner_search|username|ticket 12|{"field": "username"}|ada` {
		t.Fatalf("audit row = %q", got)
	}

	_, body = b.admin(http.MethodGet, searchPath("github_login", "zz-it-l", ""))
	if !strings.Contains(body, "zz-it-one") || strings.Contains(body, "zz-it-two") {
		t.Fatalf("login prefix is case-insensitive and exact on the prefix: %s", body)
	}
	if got := psqlAsOwner(t, "SELECT count(*) FROM admin_audit WHERE action = 'learner.search' AND target_id = 'github_login' AND reason IS NULL"); got != "1" {
		t.Fatalf("login search audit rows = %s", got)
	}
	if got := psqlAsOwner(t, "SELECT count(*) FROM admin_audit WHERE row_to_json(admin_audit)::text ILIKE '%zz-it%' AND action = 'learner.search'"); got != "0" {
		t.Fatalf("a query leaked into %s audit rows", got)
	}
}

func TestSearchTreatsWildcardsAsLiterals(t *testing.T) {
	seedLearners(t)
	b := signedInBrowser(t)
	for _, q := range []string{"zz_", "zz%", "%%", "z_-it"} {
		if _, body := b.admin(http.MethodGet, searchPath("username", q, "")); !strings.Contains(body, "No learner found.") {
			t.Errorf("%q matched as a wildcard: %s", q, body)
		}
	}
}

func TestSearchByEmailIsExactAndLeavesNoAddressInTheAudit(t *testing.T) {
	seedLearners(t)
	b := signedInBrowser(t)

	_, body := b.admin(http.MethodGet, searchPath("email", "ZZ.IT.ONE@Example.com", ""))
	if !strings.Contains(body, "zz-it-one") || strings.Contains(body, "zz-it-two") {
		t.Fatalf("exact email: %s", body)
	}
	for _, q := range []string{"zz.it.one", "zz.it.one@example", "example.com", "@example.com", "zz.it.%"} {
		if _, body := b.admin(http.MethodGet, searchPath("email", q, "")); !strings.Contains(body, "No learner found.") {
			t.Errorf("email query %q matched by part: %s", q, body)
		}
	}
	if got := psqlAsOwner(t, "SELECT count(*) FROM admin_audit WHERE action = 'learner.search' AND target_id = 'email'"); got != "6" {
		t.Fatalf("email search audit rows = %s, want 6", got)
	}
	if got := psqlAsOwner(t, "SELECT count(*) FROM admin_audit WHERE operator_login = 'ada' AND (row_to_json(admin_audit)::text LIKE '%@%' OR row_to_json(admin_audit)::text ILIKE '%zz.it%')"); got != "0" {
		t.Fatalf("an email-like value reached %s audit rows", got)
	}
}

func TestRefusedSearchesWriteNoAudit(t *testing.T) {
	seedLearners(t)
	b := signedInBrowser(t)
	for _, p := range []string{searchPath("password", "abcd", ""), searchPath("username", "z", ""), searchPath("email", "", "")} {
		if resp, _ := b.admin(http.MethodGet, p); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d", p, resp.StatusCode)
		}
	}
	if n := psqlAsOwner(t, "SELECT count(*) FROM admin_audit WHERE operator_login = 'ada'"); n != "0" {
		t.Fatalf("refused searches wrote %s audit rows", n)
	}
}

func TestViewShowsTheLearnerAndAuditsIt(t *testing.T) {
	id1, id2 := seedLearners(t)
	b := signedInBrowser(t)

	resp, body := b.admin(http.MethodGet, "/learners/"+id1+"?reason=support+case+7")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("view: %d %s", resp.StatusCode, body)
	}
	for _, want := range []string{"@zz-it-one", "ZZ-It-Login", lookupMail1, "ZZ IT Role", "1 of 1", "IT ticket", "3 of 3", "Active", "Not available yet"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if got := psqlAsOwner(t, "SELECT action || '|' || target_type || '|' || target_id || '|' || coalesce(reason, '') || '|' || operator_login FROM admin_audit WHERE action = 'learner.view'"); got != "learner.view|learner|"+id1+"|support case 7|ada" {
		t.Fatalf("audit row = %q", got)
	}

	_, body = b.admin(http.MethodGet, "/learners/"+id2)
	if !strings.Contains(body, "Suspended since") {
		t.Fatalf("suspended learner: %s", body)
	}
	if got := psqlAsOwner(t, "SELECT count(*) FROM admin_audit WHERE action = 'learner.view' AND reason IS NULL AND target_id = '"+id2+"'"); got != "1" {
		t.Fatalf("second view audit rows = %s", got)
	}
	if got := psqlAsOwner(t, "SELECT count(*) FROM admin_audit WHERE operator_login = 'ada' AND (row_to_json(admin_audit)::text LIKE '%@%' OR row_to_json(admin_audit)::text ILIKE '%zz-it-%')"); got != "0" {
		t.Fatalf("personal data reached %s audit rows", got)
	}
}

func TestViewOfAnUnknownLearnerIs404AndWritesNothing(t *testing.T) {
	seedLearners(t)
	b := signedInBrowser(t)
	for _, id := range []string{"0192b7c8-0000-7000-8000-0000000000ff", "not-a-uuid", "zz-it-one"} {
		if resp, _ := b.admin(http.MethodGet, "/learners/"+id); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %d", id, resp.StatusCode)
		}
	}
	if n := psqlAsOwner(t, "SELECT count(*) FROM admin_audit WHERE operator_login = 'ada'"); n != "0" {
		t.Fatalf("unknown learners wrote %s audit rows", n)
	}
}

func TestLookupPagesCarryTheSecureHeaders(t *testing.T) {
	id1, _ := seedLearners(t)
	b := signedInBrowser(t)
	for _, p := range []string{"/learners", "/learners/" + id1} {
		resp, _ := b.admin(http.MethodGet, p)
		if resp.Header.Get("Cache-Control") != "no-store" || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'none'") {
			t.Errorf("%s headers: %v", p, resp.Header)
		}
	}
}

func TestLookupHasNoWriteRoute(t *testing.T) {
	id1, _ := seedLearners(t)
	b := signedInBrowser(t)
	csrf := b.csrf()
	for _, p := range []string{"/learners", "/learners/" + id1} {
		resp := b.postForm(p, url.Values{"csrf_token": {csrf}}, nil)
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("POST %s = %d", p, resp.StatusCode)
		}
	}
}
