//go:build integration

package admin_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/kibanyadev/dojo/internal/admin"
)

func cleanDeleteFixtures(t *testing.T) {
	t.Helper()
	psqlAsOwner(t, "DELETE FROM email_outbox WHERE kind = 'E8' AND (ref IN (SELECT target_id FROM admin_audit WHERE action = 'learner.delete') OR to_address LIKE '%@zz-it-del.example')")
	psqlAsOwner(t, "DELETE FROM admin_audit WHERE action = 'learner.delete'")
}

func deleteForm(csrf, reason, typed string) url.Values {
	return url.Values{"csrf_token": {csrf}, "reason": {reason}, "typed": {typed}}
}

func TestDeleteEndToEnd(t *testing.T) {
	cleanDeleteFixtures(t)
	t.Cleanup(func() { cleanDeleteFixtures(t) })
	id1, id2 := seedLearners(t)
	seedAPICredentials(t)
	psqlAsOwner(t, "UPDATE learners SET email = 'victim@zz-it-del.example' WHERE id = '"+id1+"'")
	b := signedInBrowser(t)
	// An older audit row about the other learner must survive.
	if resp := b.postForm("/learners/"+id2+"/unsuspend", url.Values{"csrf_token": {b.csrf()}, "reason": {"appeal"}}, nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("unsuspend: %d", resp.StatusCode)
	}

	_, body := b.admin(http.MethodGet, "/learners/"+id1+"/delete")
	if !strings.Contains(body, "What will change") || !strings.Contains(body, `name="typed"`) {
		t.Fatalf("confirm page: %s", body)
	}
	if n := psqlAsOwner(t, "SELECT count(*) FROM admin_audit WHERE action = 'learner.delete'"); n != "0" {
		t.Fatalf("the confirm page wrote %s audit rows", n)
	}
	if got := psqlAsOwner(t, "SELECT count(*) FROM learners WHERE id = '"+id1+"'"); got != "1" {
		t.Fatal("the confirm page deleted the learner")
	}

	resp := b.postForm("/learners/"+id1+"/delete", deleteForm(b.csrf(), "erasure by email", "zz-it-one"), nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/learners?deleted=1" {
		t.Fatalf("delete: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	for _, q := range []string{
		"SELECT count(*) FROM learners WHERE id = '" + id1 + "'",
		"SELECT count(*) FROM sessions WHERE learner_id = '" + id1 + "'",
		"SELECT count(*) FROM cli_tokens WHERE learner_id = '" + id1 + "'",
		"SELECT count(*) FROM ticket_progress WHERE learner_id = '" + id1 + "'",
	} {
		if got := psqlAsOwner(t, q); got != "0" {
			t.Errorf("%s = %s, want 0", q, got)
		}
	}
	if c, br := apiAnswers(t); c != http.StatusUnauthorized || br != http.StatusUnauthorized {
		t.Fatalf("after delete: cookie %d, bearer %d, want 401 both", c, br)
	}
	if got := psqlAsOwner(t, "SELECT count(*) FROM email_outbox WHERE kind = 'E8' AND ref = '"+id1+"' AND to_address = 'victim@zz-it-del.example' AND sent_at IS NULL"); got != "1" {
		t.Fatalf("E8 rows = %s, want 1", got)
	}
	if got := psqlAsOwner(t, "SELECT operator_login || '|' || target_type || '|' || reason || '|' || (before->>'deleted') || '|' || (after->>'deleted') FROM admin_audit WHERE action = 'learner.delete' AND target_id = '"+id1+"'"); got != "ada|learner|erasure by email|false|true" {
		t.Fatalf("audit row = %q", got)
	}
	if got := psqlAsOwner(t, "SELECT count(*) FROM admin_audit WHERE action = 'learner.delete'"); got != "1" {
		t.Fatalf("delete audit rows = %s, want 1", got)
	}
	if got := psqlAsOwner(t, "SELECT count(*) FROM admin_audit WHERE row_to_json(admin_audit)::text LIKE '%@%' OR row_to_json(admin_audit)::text LIKE '%zz-it-one%'"); got != "0" {
		t.Fatalf("personal data reached %s audit rows", got)
	}
	if got := psqlAsOwner(t, "SELECT count(*) FROM admin_audit WHERE action = 'learner.unsuspend' AND target_id = '"+id2+"'"); got != "1" {
		t.Fatalf("history of the other learner: %s rows, want 1", got)
	}
	if got := psqlAsOwner(t, "SELECT count(*) FROM learners WHERE id = '"+id2+"'"); got != "1" {
		t.Fatal("the other learner was deleted")
	}

	if resp, _ := b.admin(http.MethodGet, "/learners?deleted=1"); resp.StatusCode != http.StatusOK {
		t.Fatalf("search page: %d", resp.StatusCode)
	}
	if _, body := b.admin(http.MethodGet, "/learners?deleted=1"); !strings.Contains(body, "Account deleted.") || strings.Contains(body, "zz-it-one") {
		t.Fatalf("notice: %s", body)
	}

	// A repeat writes nothing: the learner page and the delete are gone.
	if resp, _ := b.admin(http.MethodGet, "/learners/"+id1+"/delete"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET after delete: %d", resp.StatusCode)
	}
	if resp := b.postForm("/learners/"+id1+"/delete", deleteForm(b.csrf(), "again", "zz-it-one"), nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("repeat: %d", resp.StatusCode)
	}
	if got := psqlAsOwner(t, "SELECT (SELECT count(*) FROM admin_audit WHERE action = 'learner.delete') || '|' || (SELECT count(*) FROM email_outbox WHERE kind = 'E8' AND ref = '"+id1+"')"); got != "1|1" {
		t.Fatalf("after repeat: audit|E8 = %s, want 1|1", got)
	}
}

func TestDeleteWithoutEmailQueuesNoE8(t *testing.T) {
	cleanDeleteFixtures(t)
	t.Cleanup(func() { cleanDeleteFixtures(t) })
	id1, _ := seedLearners(t)
	psqlAsOwner(t, "UPDATE learners SET email = NULL WHERE id = '"+id1+"'")
	b := signedInBrowser(t)
	if resp := b.postForm("/learners/"+id1+"/delete", deleteForm(b.csrf(), "erasure", "zz-it-one"), nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	if got := psqlAsOwner(t, "SELECT count(*) FROM email_outbox WHERE kind = 'E8' AND ref = '"+id1+"'"); got != "0" {
		t.Fatalf("E8 rows = %s, want 0", got)
	}
	if got := psqlAsOwner(t, "SELECT count(*) FROM admin_audit WHERE action = 'learner.delete' AND target_id = '"+id1+"'"); got != "1" {
		t.Fatalf("audit rows = %s, want 1", got)
	}
}

func TestDeleteRefusalsWriteNothing(t *testing.T) {
	cleanDeleteFixtures(t)
	t.Cleanup(func() { cleanDeleteFixtures(t) })
	id1, _ := seedLearners(t)
	b := signedInBrowser(t)
	tok := b.csrf()
	cases := []struct {
		name string
		path string
		form url.Values
		hdr  map[string]string
		want int
	}{
		{"no reason", "/learners/" + id1 + "/delete", deleteForm(tok, "", "zz-it-one"), nil, http.StatusBadRequest},
		{"blank reason", "/learners/" + id1 + "/delete", deleteForm(tok, "   ", "zz-it-one"), nil, http.StatusBadRequest},
		{"reason too long", "/learners/" + id1 + "/delete", deleteForm(tok, strings.Repeat("r", 1001), "zz-it-one"), nil, http.StatusBadRequest},
		{"no typed username", "/learners/" + id1 + "/delete", deleteForm(tok, "x", ""), nil, http.StatusBadRequest},
		{"wrong typed username", "/learners/" + id1 + "/delete", deleteForm(tok, "x", "zz-it-two"), nil, http.StatusBadRequest},
		{"no csrf", "/learners/" + id1 + "/delete", deleteForm("", "x", "zz-it-one"), nil, http.StatusForbidden},
		{"bad csrf", "/learners/" + id1 + "/delete", deleteForm("nope", "x", "zz-it-one"), nil, http.StatusForbidden},
		{"cross-site", "/learners/" + id1 + "/delete", deleteForm(tok, "x", "zz-it-one"), map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"unknown learner", "/learners/0192b7c8-0000-7000-8000-0000000000ff/delete", deleteForm(tok, "x", "zz-it-one"), nil, http.StatusNotFound},
		{"not a uuid", "/learners/zz-it-one/delete", deleteForm(tok, "x", "zz-it-one"), nil, http.StatusNotFound},
	}
	for _, c := range cases {
		if resp := b.postForm(c.path, c.form, c.hdr); resp.StatusCode != c.want {
			t.Errorf("%s: %d, want %d", c.name, resp.StatusCode, c.want)
		}
	}
	if got := psqlAsOwner(t, "SELECT (SELECT count(*) FROM admin_audit WHERE action = 'learner.delete') || '|' || (SELECT count(*) FROM learners WHERE id = '"+id1+"') || '|' || (SELECT count(*) FROM email_outbox WHERE kind = 'E8')"); !strings.HasPrefix(got, "0|1|") {
		t.Fatalf("refused requests changed state: %s", got)
	}
}

func TestDeleteNeedsAnOperatorSession(t *testing.T) {
	id1, _ := seedLearners(t)
	seedOperator(t, "1001", "ada", false)
	b := newOperatorBrowser(t)
	if resp, _ := b.admin(http.MethodGet, "/learners/"+id1+"/delete"); resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/signin" {
		t.Fatalf("GET: %d", resp.StatusCode)
	}
	resp := b.postForm("/learners/"+id1+"/delete", url.Values{"reason": {"x"}, "typed": {"zz-it-one"}}, nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/signin" {
		t.Fatalf("POST: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if got := psqlAsOwner(t, "SELECT count(*) FROM learners WHERE id = '"+id1+"'"); got != "1" {
		t.Fatal("a non-operator deleted a learner")
	}
}

func TestAFailedAuditRollsTheDeletionBack(t *testing.T) {
	id1, _ := seedLearners(t)
	seedAPICredentials(t)
	store := admin.PGLearners{Pool: adminPool(t)}
	ghost := admin.Operator{ID: "0192b7c8-0000-7000-8000-0000000000ee", Login: "ghost"} // not in operators: the audit FK fails
	if err := store.Delete(context.Background(), ghost, id1, "x"); err == nil {
		t.Fatal("delete with an unwritable audit row succeeded")
	}
	if got := psqlAsOwner(t, "SELECT (SELECT count(*) FROM learners WHERE id = '"+id1+"') || '|' || (SELECT count(*) FROM sessions WHERE learner_id = '"+id1+"') || '|' || (SELECT count(*) FROM cli_tokens WHERE learner_id = '"+id1+"') || '|' || (SELECT count(*) FROM email_outbox WHERE kind = 'E8' AND ref = '"+id1+"')"); got != "1|1|1|0" {
		t.Fatalf("learner|sessions|tokens|E8 after a failed audit = %s, want 1|1|1|0", got)
	}
}
