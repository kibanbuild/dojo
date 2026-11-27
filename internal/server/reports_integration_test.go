//go:build integration

package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kibanyadev/dojo/internal/server"
	"github.com/kibanyadev/dojo/internal/server/repo"
)

const reportsTo = "reports@dojo.test"

// post sends POST /v1/reports as the browser: its cookie, plus the CSRF header when csrf is set.
func (b *browser) post(path, body string, csrf bool) (int, string) {
	b.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, apiURL(b.t)+path, bytes.NewReader([]byte(body)))
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
	return resp.StatusCode, string(out)
}

// postRaw sends a body as the CLI does: bearer, any content type.
func postRaw(t *testing.T, tok, ctype, body string) (int, string, http.Header) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, apiURL(t)+"/v1/reports", strings.NewReader(body))
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := newBrowser(t).c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out), resp.Header
}

func reportID(t *testing.T, code int, body string) string {
	t.Helper()
	var out struct{ ID string }
	if code != http.StatusCreated || json.Unmarshal([]byte(body), &out) != nil || len(out.ID) != 36 {
		t.Fatalf("report: %d %q", code, body)
	}
	return out.ID
}

type reportMail struct {
	Subject string
	ReplyTo []struct{ Address string }
	Text    string
}

// reportMailFor waits for the mail about one report and returns it.
func reportMailFor(t *testing.T, id string) reportMail {
	t.Helper()
	waitFor(t, "the report mail", func() bool {
		for _, m := range inboxOf(t, reportsTo) {
			if strings.HasSuffix(m.Subject, " "+id[len(id)-8:]) {
				return true
			}
		}
		return false
	})
	for _, m := range inboxOf(t, reportsTo) {
		if strings.HasSuffix(m.Subject, " "+id[len(id)-8:]) {
			var full reportMail
			mailpitGet(t, "/api/v1/message/"+m.ID, &full)
			return full
		}
	}
	t.Fatal("report mail vanished")
	return reportMail{}
}

func reportsOf(t *testing.T) string {
	t.Helper()
	return asOwner(t, "SELECT count(*) FROM ticket_reports r JOIN learners l ON l.id = r.learner_id WHERE l.github_id = 1001")
}

func TestReportRouteAuthAndValidation(t *testing.T) {
	_, tok := ticketFixture(t)
	good := `{"kind":"broken_ticket","item_id":"foundations/a","message":"The check fails."}`

	if code, _, _ := postRaw(t, "dojo_"+strings.Repeat("a", 43), "application/json", good); code != http.StatusUnauthorized {
		t.Fatalf("unknown token: %d", code)
	}
	anon := newBrowser(t)
	if code, _ := anon.post("/v1/reports", good, true); code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", code)
	}

	ada := newBrowser(t)
	if resp := ada.signIn("ada", "", "", false); resp.Header.Get("Location") != "/app" {
		t.Fatalf("sign-in: %q", resp.Header.Get("Location"))
	}
	if code, body := ada.post("/v1/reports", good, false); code != http.StatusForbidden || body != "{\"error\":\"csrf_rejected\"}\n" {
		t.Fatalf("cookie without CSRF: %d %q", code, body)
	}
	if n := reportsOf(t); n != "0" {
		t.Fatalf("a refused report was stored: %s", n)
	}

	for _, tt := range []struct {
		name, ctype, body string
		want              int
		err               string
	}{
		{"not json", "text/plain", good, 400, "bad_request"},
		{"empty body", "application/json", ``, 400, "bad_request"},
		{"unknown kind", "application/json", `{"kind":"other","message":"x"}`, 400, "bad_request"},
		{"broken ticket without item", "application/json", `{"kind":"broken_ticket","message":"x"}`, 400, "bad_request"},
		{"empty message", "application/json", `{"kind":"setup_help","message":"  "}`, 400, "bad_request"},
		{"message over 2000", "application/json", `{"kind":"setup_help","message":"` + strings.Repeat("a", 2001) + `"}`, 400, "bad_request"},
		{"NUL in message", "application/json", `{"kind":"setup_help","message":"a\u0000b"}`, 400, "bad_request"},
		{"body over 8 KB", "application/json", `{"kind":"setup_help","message":"x","include_output":false,"pad":"` + strings.Repeat("a", 9000) + `"}`, 400, "bad_request"},
		{"client output is refused", "application/json", `{"kind":"setup_help","message":"x","last_check_output":"forged"}`, 400, "bad_request"},
		{"unknown item", "application/json", `{"kind":"broken_ticket","item_id":"foundations/nope","message":"x"}`, 404, "not_found"},
		{"item that is not a ticket id", "application/json", `{"kind":"setup_help","item_id":"../x","message":"x"}`, 404, "not_found"},
	} {
		code, body, _ := postRaw(t, tok, tt.ctype, tt.body)
		if code != tt.want || body != "{\"error\":\""+tt.err+"\"}\n" {
			t.Errorf("%s: %d %q", tt.name, code, body)
		}
	}
	if n := reportsOf(t); n != "0" {
		t.Fatalf("a refused report was stored: %s", n)
	}

	// A learner who cannot sign in cannot report: suspended is 403 on both credentials.
	asOwner(t, "UPDATE learners SET suspended_at = now() WHERE github_id = 1001")
	if code, body, _ := postRaw(t, tok, "application/json", good); code != http.StatusForbidden || !strings.Contains(body, "account_suspended") {
		t.Fatalf("suspended: %d %q", code, body)
	}
	asOwner(t, "UPDATE learners SET suspended_at = NULL WHERE github_id = 1001")

	// The cookie with the CSRF header is accepted.
	if code, body := ada.post("/v1/reports", good, true); code != http.StatusCreated {
		t.Fatalf("cookie with CSRF: %d %q", code, body)
	}
	if n := reportsOf(t); n != "1" {
		t.Fatalf("reports after the cookie call: %s", n)
	}
	idleReports(t)
}

func TestReportStoresOnlyWhatTheServerKnows(t *testing.T) {
	_, tok := ticketFixture(t)
	asOwner(t, "UPDATE learners SET email = 'ada@example.test' WHERE github_id = 1001")
	asOwner(t, "INSERT INTO ticket_progress (org_id, learner_id, item_id, state, first_started_at, last_check_output) "+
		"SELECT org_id, id, 'foundations/a', 'started', now(), 'FAIL: owner is root' FROM learners WHERE github_id = 1001")
	asOwner(t, "INSERT INTO doctor_results (org_id, learner_id, check_id, status, text, fix, source, checked_at) "+
		"SELECT l.org_id, l.id, c.cid, c.cstatus, c.ctext, '', 'cli', now() FROM learners l, (VALUES "+
		"('disk', 'ok', 'Disk is fine'), ('lima', 'fail', 'Lima is not installed'), ('git', 'warn', 'Git is old')) AS c(cid, cstatus, ctext) WHERE l.github_id = 1001")
	clearInbox(t)

	// With include_output the API copies the learner's own output for that ticket.
	code, body, _ := postRaw(t, tok, "application/json", `{"kind":"broken_ticket","item_id":"foundations/a","message":"  The check fails.\n","include_output":true}`)
	id := reportID(t, code, body)
	row := "SELECT kind || '|' || item_id || '|' || cli_version || '|' || message || '|' || coalesce(last_check_output, '<null>') || '|' || doctor_summary FROM ticket_reports WHERE id = '" + id + "'"
	if got := asOwner(t, row); got != "broken_ticket|foundations/a|v0.0.0-test|The check fails.|FAIL: owner is root|" {
		t.Fatalf("row: %q", got)
	}
	m := reportMailFor(t, id)
	if m.Subject != "[dojo report] broken_ticket foundations/a "+id[len(id)-8:] || len(m.ReplyTo) != 1 || m.ReplyTo[0].Address != "ada@example.test" {
		t.Fatalf("subject %q, reply-to %+v", m.Subject, m.ReplyTo)
	}
	for _, want := range []string{"The check fails.", "Learner: ada", "CLI version: v0.0.0-test", "FAIL: owner is root"} {
		if !strings.Contains(m.Text, want) {
			t.Errorf("mail text misses %q: %q", want, m.Text)
		}
	}
	if strings.Contains(m.Text, "Doctor summary") {
		t.Errorf("a broken-ticket mail carries the doctor summary: %q", m.Text)
	}
	waitFor(t, "emailed_at", func() bool {
		return asOwner(t, "SELECT emailed_at IS NOT NULL FROM ticket_reports WHERE id = '"+id+"'") == "t"
	})

	// Without include_output nothing is copied.
	code, body, _ = postRaw(t, tok, "application/json", `{"kind":"broken_ticket","item_id":"foundations/a","message":"Again."}`)
	id2 := reportID(t, code, body)
	if got := asOwner(t, "SELECT last_check_output IS NULL FROM ticket_reports WHERE id = '"+id2+"'"); got != "t" {
		t.Fatalf("output copied without being asked: %s", got)
	}

	// Setup help copies the doctor results that are not ok, and a learner with no address gets no Reply-To.
	asOwner(t, "UPDATE learners SET email = NULL WHERE github_id = 1001")
	code, body, _ = postRaw(t, tok, "application/json", `{"kind":"setup_help","message":"Lima will not start.","include_output":true}`)
	id3 := reportID(t, code, body)
	if got := asOwner(t, "SELECT item_id IS NULL AND last_check_output IS NULL AND replace(doctor_summary, E'\\n', ' / ') = 'git: Git is old / lima: Lima is not installed' FROM ticket_reports WHERE id = '"+id3+"'"); got != "t" {
		t.Fatalf("setup help row: %s", asOwner(t, "SELECT item_id, doctor_summary FROM ticket_reports WHERE id = '"+id3+"'"))
	}
	m = reportMailFor(t, id3)
	if m.Subject != "[dojo report] setup_help - "+id3[len(id3)-8:] || len(m.ReplyTo) != 0 || !strings.Contains(strings.ReplaceAll(m.Text, "\r\n", "\n"), "git: Git is old\nlima: Lima is not installed") {
		t.Fatalf("setup help mail: %q %+v %q", m.Subject, m.ReplyTo, m.Text)
	}
}

func TestReportRateLimit(t *testing.T) {
	_, tok := ticketFixture(t)
	good := `{"kind":"setup_help","message":"Stuck."}`
	for i := range 5 {
		if code, body, _ := postRaw(t, tok, "application/json", good); code != http.StatusCreated {
			t.Fatalf("report %d: %d %q", i+1, code, body)
		}
	}
	code, body, h := postRaw(t, tok, "application/json", good)
	secs, _ := strconv.Atoi(h.Get("Retry-After"))
	if code != http.StatusTooManyRequests || body != "{\"error\":\"rate_limited\"}\n" || secs < 1 || secs > 3600 {
		t.Fatalf("sixth in the hour: %d %q Retry-After %q", code, body, h.Get("Retry-After"))
	}
	if n := reportsOf(t); n != "5" {
		t.Fatalf("reports stored: %s", n)
	}

	// A day limit: 20 reports older than an hour (already emailed, so the sender leaves them alone).
	asOwner(t, "DELETE FROM ticket_reports")
	asOwner(t, "INSERT INTO ticket_reports (org_id, learner_id, kind, message, created_at, emailed_at) "+
		"SELECT org_id, id, 'setup_help', 'old', now() - interval '2 hours', now() FROM learners, generate_series(1, 20) WHERE github_id = 1001")
	code, _, h = postRaw(t, tok, "application/json", good)
	secs, _ = strconv.Atoi(h.Get("Retry-After"))
	if code != http.StatusTooManyRequests || secs < 22*3600-60 || secs > 22*3600+60 {
		t.Fatalf("21st in the day: %d Retry-After %q (want about 22h)", code, h.Get("Retry-After"))
	}

	// Another learner has their own budget.
	resetLearners(t, "1002")
	signUp(t, "bob")
	bob := cliToken(t, newCLIClient(t), "bob")
	if code, body, _ := postRaw(t, bob, "application/json", good); code != http.StatusCreated {
		t.Fatalf("bob: %d %q", code, body)
	}
}

// A send that fails leaves the report unsent for the next pass, a row another task holds is skipped, and the
// report arrives once. The running API sweeps too, so this test never needs to win a race: whoever sends it,
// it ends emailed exactly once.
func TestReportForwardRetriesAFailedSend(t *testing.T) {
	_, tok := ticketFixture(t)
	clearInbox(t)
	code, body, _ := postRaw(t, tok, "application/json", `{"kind":"setup_help","message":"Retry me."}`)
	id := reportID(t, code, body)

	ctx := context.Background()
	store, err := repo.Open(ctx, apiDBURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	other, err := repo.Open(ctx, apiDBURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()

	errSend := errors.New("send failed")
	var skip []pgtype.UUID
	for range 20 {
		_, found, err := store.ForwardReport(ctx, skip, func(ctx context.Context, r repo.Report) error {
			if r.ID.String() != id {
				return errSend // someone else's row: leave it
			}
			// Held under this task's lock: a second claimer must not be handed it.
			_, _, err := other.ForwardReport(ctx, nil, func(_ context.Context, r2 repo.Report) error {
				if r2.ID == r.ID {
					t.Errorf("a locked report was claimed twice")
				}
				return errSend
			})
			if err != nil && !errors.Is(err, errSend) {
				t.Errorf("second claimer: %v", err)
			}
			return errSend
		})
		if !errors.Is(err, errSend) || !found {
			break
		}
		// The failed row is unsent again; skip it and look at the next one.
		var u pgtype.UUID
		if scanErr := u.Scan(id); scanErr != nil {
			t.Fatal(scanErr)
		}
		skip = []pgtype.UUID{u}
	}
	m := reportMailFor(t, id)
	if !strings.Contains(m.Text, "Retry me.") {
		t.Fatalf("mail: %q", m.Text)
	}
	waitFor(t, "emailed_at", func() bool {
		return asOwner(t, "SELECT emailed_at IS NOT NULL FROM ticket_reports WHERE id = '"+id+"'") == "t"
	})
	idleReports(t)
	n := 0
	for _, msg := range inboxOf(t, reportsTo) {
		if strings.HasSuffix(msg.Subject, " "+id[len(id)-8:]) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("mails for the report: %d", n)
	}
}

// idleReports waits until no report is unsent, so none lands in a later test.
func idleReports(t *testing.T) {
	t.Helper()
	waitFor(t, "no unsent reports", func() bool {
		return asOwner(t, "SELECT count(*) FROM ticket_reports WHERE emailed_at IS NULL") == "0"
	})
}
