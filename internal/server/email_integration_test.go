//go:build integration

package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kibanbuild/dojo/internal/server/repo"
)

type mailpitMessage struct {
	ID      string
	Subject string
	From    struct{ Address string }
	To      []struct{ Address string }
}

func mailpitURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("DOJO_MAILPIT_URL")
	if u == "" {
		t.Fatal("DOJO_MAILPIT_URL is not set: run `make test-integration`")
	}
	return u
}

func mailpitGet(t *testing.T, path string, out any) {
	t.Helper()
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Get(mailpitURL(t) + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("mailpit %s: %d", path, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatal(err)
	}
}

// inboxOf lists the messages Mailpit holds for an address.
func inboxOf(t *testing.T, addr string) []mailpitMessage {
	t.Helper()
	var res struct{ Messages []mailpitMessage }
	mailpitGet(t, "/api/v1/search?query="+url.QueryEscape("to:"+addr), &res)
	return res.Messages
}

func clearInbox(t *testing.T) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, mailpitURL(t)+"/api/v1/messages", nil)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

// waitFor polls until cond holds; the sender sweeps every few seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// idle waits until the outbox has nothing unsent, so no mail from an earlier test lands in this one.
func idle(t *testing.T) {
	t.Helper()
	waitFor(t, "an idle outbox", func() bool {
		return asOwner(t, "SELECT count(*) FROM email_outbox WHERE sent_at IS NULL AND to_address IS NOT NULL AND learner_id IS NOT NULL AND kind = 'E1'") == "0"
	})
}

func TestWelcomeMailGoesOutOnFirstSignInOnly(t *testing.T) {
	const gh, addr = "1004", "z@example.test"
	resetLearners(t, gh)
	idle(t)
	clearInbox(t)

	b := newBrowser(t)
	if resp := b.signIn("z", "", "age=on&terms=on", false); resp.Header.Get("Location") != "/app" {
		t.Fatalf("sign-in: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	learner := asOwner(t, "SELECT id FROM learners WHERE github_id = "+gh)
	if got := asOwner(t, "SELECT count(*) FROM email_outbox WHERE kind = 'E1' AND ref = '"+learner+"' AND learner_id = '"+learner+"'"); got != "1" {
		t.Fatalf("queued E1 rows for the new learner: %s", got)
	}
	waitFor(t, "the welcome mail", func() bool { return len(inboxOf(t, addr)) > 0 })

	msgs := inboxOf(t, addr)
	if len(msgs) != 1 || msgs[0].Subject != "Welcome to Dojo" || msgs[0].From.Address != "carmen@dojo.test" {
		t.Fatalf("inbox: %+v", msgs)
	}
	var full struct{ Text, HTML string }
	mailpitGet(t, "/api/v1/message/"+msgs[0].ID, &full)
	username := asOwner(t, "SELECT username FROM learners WHERE github_id = "+gh)
	pathTitle := asOwner(t, "SELECT title FROM paths WHERE status = 'available' ORDER BY position LIMIT 1")
	for name, body := range map[string]string{"text": full.Text, "html": full.HTML} {
		if !strings.Contains(body, username) || !strings.Contains(body, "/app") {
			t.Errorf("%s part misses the username or the link: %q", name, body)
		}
		if pathTitle != "" && !strings.Contains(body, pathTitle+" path") {
			t.Errorf("%s part does not name the path %q: %q", name, pathTitle, body)
		}
	}

	// A sent row keeps its trail and drops the address (design 3.3).
	waitFor(t, "the row to be marked sent", func() bool {
		return asOwner(t, "SELECT sent_at IS NOT NULL AND to_address IS NULL FROM email_outbox WHERE ref = '"+learner+"'") == "t"
	})
	if got := asOwner(t, "SELECT count(*) FROM email_log WHERE learner_id = '"+learner+"' AND kind = 'E1'"); got != "1" {
		t.Fatalf("email_log rows: %s", got)
	}

	// A returning sign-in queues nothing, so no second mail follows.
	again := newBrowser(t)
	if resp := again.signIn("z", "", "", false); resp.Header.Get("Location") != "/app" {
		t.Fatalf("returning: %q", resp.Header.Get("Location"))
	}
	if got := asOwner(t, "SELECT count(*) FROM email_outbox WHERE learner_id = '"+learner+"'"); got != "1" {
		t.Fatalf("outbox rows after a returning sign-in: %s", got)
	}
	idle(t)
	if n := len(inboxOf(t, addr)); n != 1 {
		t.Fatalf("messages after a returning sign-in: %d", n)
	}
}

// mark_email_sent behavior as dojo_api: E8 is deleted, others keep the row without an address, and a
// second call or an unknown id is a no-op.
func TestMarkEmailSent(t *testing.T) {
	const gh, marker = "1005", "it-mark-sent"
	clean := func() {
		asOwner(t, "DELETE FROM email_outbox WHERE ref LIKE '"+marker+"%'")
		asOwner(t, "DELETE FROM learners WHERE github_id = "+gh)
	}
	clean()
	t.Cleanup(clean)
	api := func(sql string) string {
		t.Helper()
		out, err := psqlAs(t, "dojo_api", envExample(t, "DOJO_API_DB_PASSWORD"), sql)
		if err != nil {
			t.Fatalf("%s: %v %s", sql, err, out)
		}
		return strings.Fields(out + " ")[0]
	}
	learner := strings.Fields(asOwner(t, "INSERT INTO learners (org_id, github_id, github_login, username, email, age_confirmed_at, terms_accepted_at, terms_version) "+
		"SELECT id, "+gh+", 'mk', 'mk-sent', 'mk@example.test', now(), now(), 'x' FROM orgs WHERE slug = 'public' RETURNING id"))[0]
	// E2 and E8 have no template yet, so the sender leaves these rows alone.
	e2 := strings.Fields(asOwner(t, "INSERT INTO email_outbox (learner_id, to_address, kind, ref) VALUES ('"+learner+"', 'mk@example.test', 'E2', '"+marker+"-e2') RETURNING id"))[0]
	e8 := strings.Fields(asOwner(t, "INSERT INTO email_outbox (learner_id, to_address, kind, ref) VALUES (NULL, 'gone@example.test', 'E8', '"+marker+"-e8') RETURNING id"))[0]

	if got := api(fmt.Sprintf("SELECT mark_email_sent('%s')", e2)); got != "t" {
		t.Fatalf("mark E2: %s", got)
	}
	if got := asOwner(t, "SELECT sent_at IS NOT NULL AND to_address IS NULL FROM email_outbox WHERE id = '"+e2+"'"); got != "t" {
		t.Fatalf("E2 row after mark: %s", got)
	}
	if got := asOwner(t, "SELECT count(*) FROM email_log WHERE learner_id = '"+learner+"' AND kind = 'E2'"); got != "1" {
		t.Fatalf("email_log for E2: %s", got)
	}
	if got := api(fmt.Sprintf("SELECT mark_email_sent('%s')", e2)); got != "f" {
		t.Fatalf("second mark: %s", got)
	}
	if got := asOwner(t, "SELECT count(*) FROM email_log WHERE learner_id = '"+learner+"'"); got != "1" {
		t.Fatalf("a second mark logged again: %s", got)
	}

	if got := api(fmt.Sprintf("SELECT mark_email_sent('%s')", e8)); got != "t" {
		t.Fatalf("mark E8: %s", got)
	}
	if got := asOwner(t, "SELECT count(*) FROM email_outbox WHERE id = '"+e8+"'"); got != "0" {
		t.Fatalf("E8 row survived: %s", got)
	}
	if got := api("SELECT mark_email_sent('0b0b0b0b-0000-4000-8000-000000000001')"); got != "f" {
		t.Fatalf("unknown id: %s", got)
	}
}

// The sender sends only kinds with a template: an E2 row waits, address intact.
func TestUntemplatedKindsStayQueued(t *testing.T) {
	const marker = "it-untemplated"
	clean := func() { asOwner(t, "DELETE FROM email_outbox WHERE ref = '"+marker+"'") }
	clean()
	t.Cleanup(clean)
	asOwner(t, "INSERT INTO email_outbox (learner_id, to_address, kind, ref) VALUES (NULL, 'wait@example.test', 'E2', '"+marker+"')")
	// Two sweeps' worth: a row that was going to be sent would be gone by now.
	time.Sleep(12 * time.Second)
	if got := asOwner(t, "SELECT sent_at IS NULL AND to_address = 'wait@example.test' FROM email_outbox WHERE ref = '"+marker+"'"); got != "t" {
		t.Fatalf("E2 row changed: %s", got)
	}
	if n := len(inboxOf(t, "wait@example.test")); n != 0 {
		t.Fatalf("an E2 mail was sent: %d", n)
	}
}

// ListUnsentEmails against real Postgres: a nil skip (NULL on the wire), an empty one and a real one.
// E8 rows have no template, so the running sender leaves them alone.
func TestListUnsentEmailsSkip(t *testing.T) {
	const marker = "it-list-unsent"
	clean := func() { asOwner(t, "DELETE FROM email_outbox WHERE ref LIKE '"+marker+"%'") }
	clean()
	t.Cleanup(clean)
	var ids [3]pgtype.UUID
	for i := range ids {
		out := strings.Fields(asOwner(t, fmt.Sprintf("INSERT INTO email_outbox (learner_id, to_address, kind, ref) VALUES (NULL, 'l%d@example.test', 'E8', '%s-%d') RETURNING id", i, marker, i)))[0]
		if err := ids[i].Scan(out); err != nil {
			t.Fatal(err)
		}
	}
	// Sent and address-less rows never show.
	asOwner(t, "INSERT INTO email_outbox (learner_id, to_address, kind, ref) VALUES (NULL, NULL, 'E8', '"+marker+"-noaddr')")

	ctx := context.Background()
	store, err := repo.Open(ctx, apiDBURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	mine := func(skip []pgtype.UUID) map[pgtype.UUID]bool {
		t.Helper()
		rows, err := store.ListUnsentEmails(ctx, []string{"E8"}, skip, 1000)
		if err != nil {
			t.Fatal(err)
		}
		got := map[pgtype.UUID]bool{}
		for _, r := range rows {
			for _, id := range ids {
				if r.ID == id {
					got[id] = true
				}
			}
		}
		return got
	}
	if got := mine(nil); len(got) != 3 {
		t.Fatalf("nil skip: %d of my 3 rows", len(got))
	}
	if got := mine([]pgtype.UUID{}); len(got) != 3 {
		t.Fatalf("empty skip: %d of my 3 rows", len(got))
	}
	got := mine([]pgtype.UUID{ids[1]})
	if len(got) != 2 || got[ids[1]] {
		t.Fatalf("skip of one row: %v", got)
	}
}
