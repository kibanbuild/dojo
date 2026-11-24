//go:build integration

package server_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/kibanbuild/dojo/internal/server"
)

// ticketFixture seeds the catalog with foundations/a holding three hints and signs ada up.
func ticketFixture(t *testing.T) (c *cliClient, tok string) {
	t.Helper()
	freshCatalog(t)
	seedCatalog(t)
	asOwner(t, "DELETE FROM events")
	t.Cleanup(func() { asOwner(t, "DELETE FROM events") })
	asOwner(t, "UPDATE catalog_items SET hints = ARRAY['hint one','hint two','hint three'], brief = '# Brief <script>x</script>' WHERE id = 'foundations/a'")
	resetLearners(t, "1001")
	signUp(t, "ada")
	c = newCLIClient(t)
	return c, cliToken(t, c, "ada")
}

type ticketBody struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	Role      string  `json:"role"`
	Brief     string  `json:"brief"`
	Est       int     `json:"est_minutes"`
	HintCount int     `json:"hint_count"`
	Hints     []hint  `json:"hints"`
	Progress  *progBd `json:"progress"`
}

type hint struct {
	N    int    `json:"n"`
	Text string `json:"text"`
}

type progBd struct {
	State           string  `json:"state"`
	CheckCount      int     `json:"check_count"`
	LastCheckOK     *bool   `json:"last_check_ok"`
	LastCheckAt     *string `json:"last_check_at"`
	LastCheckPassed *int    `json:"last_check_passed"`
	LastCheckTotal  *int    `json:"last_check_total"`
	LastCheckOutput *string `json:"last_check_output"`
	FirstPassedAt   *string `json:"first_passed_at"`
}

func readTicket(t *testing.T, tok, id string) ticketBody {
	t.Helper()
	code, body, h := getAs(t, tok, "/v1/tickets/"+id)
	var out ticketBody
	if code != 200 || json.Unmarshal([]byte(body), &out) != nil || h.Get("Cache-Control") != "no-store" {
		t.Fatalf("read %s: %d %q", id, code, body)
	}
	return out
}

func openHint(c *cliClient, tok, id, n string) (int, string) {
	return c.call(http.MethodPost, "/v1/tickets/"+id+"/hints/"+n, tok, nil)
}

const hintEvents = "SELECT count(*) FROM events e JOIN learners l ON l.id = e.learner_id WHERE l.github_id = 1001 AND e.type = 'hint.opened'"

func TestTicketReadIntegration(t *testing.T) {
	_, tok := ticketFixture(t)

	got := readTicket(t, tok, "foundations/a")
	if got.ID != "foundations/a" || got.Title != "Find the log" || got.Role != "foundations" || got.Est != 10 ||
		got.Brief != "# Brief <script>x</script>" || got.HintCount != 3 || len(got.Hints) != 0 || got.Progress != nil {
		t.Fatalf("no progress: %+v", got)
	}

	setProgress(t, "foundations/a", "started", "")
	asOwner(t, "UPDATE ticket_progress SET check_count = 2, last_check_at = '2026-11-23T10:31:40Z', last_check_ok = false, "+
		"last_check_passed = 3, last_check_total = 5, last_check_output = 'boom <b>' WHERE item_id = 'foundations/a'")
	p := readTicket(t, tok, "foundations/a").Progress
	if p == nil || p.State != "started" || p.CheckCount != 2 || p.LastCheckOK == nil || *p.LastCheckOK ||
		p.LastCheckAt == nil || *p.LastCheckAt != "2026-11-23T10:31:40Z" || *p.LastCheckPassed != 3 || *p.LastCheckTotal != 5 ||
		*p.LastCheckOutput != "boom <b>" || p.FirstPassedAt != nil {
		t.Fatalf("progress: %+v", p)
	}

	// Passed shows every hint, opened or not.
	asOwner(t, "UPDATE ticket_progress SET state = 'passed', first_passed_at = now() WHERE item_id = 'foundations/a'")
	if got := readTicket(t, tok, "foundations/a"); len(got.Hints) != 3 || got.Hints[2].Text != "hint three" {
		t.Fatalf("passed hints: %+v", got.Hints)
	}
}

func TestOpenHintsIntegration(t *testing.T) {
	c, tok := ticketFixture(t)
	id := "foundations/a"

	if code, body := openHint(c, tok, id, "2"); code != 409 || strings.TrimSpace(body) != `{"error":"hint_locked"}` {
		t.Fatalf("2 before 1: %d %q", code, body)
	}
	if code, body := openHint(c, tok, id, "3"); code != 409 {
		t.Fatalf("3 before 1: %d %q", code, body)
	}
	if n := asOwner(t, hintEvents); n != "0" {
		t.Fatalf("refusals wrote events: %q", n)
	}
	if code, body := openHint(c, tok, id, "1"); code != 200 || strings.TrimSpace(body) != `{"n":1,"text":"hint one"}` {
		t.Fatalf("open 1: %d %q", code, body)
	}
	if code, _ := openHint(c, tok, id, "3"); code != 409 {
		t.Fatalf("3 before 2: %d", code)
	}
	if code, body := openHint(c, tok, id, "2"); code != 200 || strings.TrimSpace(body) != `{"n":2,"text":"hint two"}` {
		t.Fatalf("open 2: %d %q", code, body)
	}
	if code, body := openHint(c, tok, id, "1"); code != 200 || strings.TrimSpace(body) != `{"n":1,"text":"hint one"}` {
		t.Fatalf("repeat: %d %q", code, body)
	}

	if n := asOwner(t, hintEvents); n != "2" {
		t.Fatalf("hint.opened events after a repeat: %q", n)
	}
	if n := asOwner(t, "SELECT count(*) FROM hint_opens h JOIN learners l ON l.id = h.learner_id WHERE l.github_id = 1001"); n != "2" {
		t.Fatalf("hint_opens: %q", n)
	}
	if p := asOwner(t, "SELECT payload::text FROM events WHERE type = 'hint.opened' ORDER BY at, id LIMIT 1"); p != `{"n": 1, "item_id": "foundations/a"}` {
		t.Fatalf("payload: %q", p)
	}
	if n := asOwner(t, "SELECT count(*) FROM ticket_progress"); n != "0" {
		t.Fatalf("a hint created a progress row: %q", n)
	}
	got := readTicket(t, tok, id)
	if len(got.Hints) != 2 || got.Hints[0].N != 1 || got.Hints[1].Text != "hint two" || got.Progress != nil {
		t.Fatalf("read after opens: %+v", got)
	}
}

func TestOpenHintConcurrentIntegration(t *testing.T) {
	c, tok := ticketFixture(t)
	const workers = 8
	codes := make(chan int, workers)
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		go func() {
			<-start
			code, _ := openHint(c, tok, "foundations/a", "1")
			codes <- code
		}()
	}
	close(start)
	for i := 0; i < workers; i++ {
		if code := <-codes; code != 200 {
			t.Fatalf("concurrent open: %d", code)
		}
	}
	if n := asOwner(t, "SELECT count(*) FROM hint_opens h JOIN learners l ON l.id = h.learner_id WHERE l.github_id = 1001 AND h.n = 1"); n != "1" {
		t.Fatalf("hint_opens rows: %q", n)
	}
	if n := asOwner(t, hintEvents); n != "1" {
		t.Fatalf("hint.opened events: %q", n)
	}
}

func TestOpenHintLeavesProgressUntouchedIntegration(t *testing.T) {
	c, tok := ticketFixture(t)
	setProgress(t, "foundations/a", "started", "running")
	const snapshot = "SELECT updated_at, last_seen_at, state, check_count FROM ticket_progress"
	before := asOwner(t, snapshot)
	if code, _ := openHint(c, tok, "foundations/a", "1"); code != 200 {
		t.Fatalf("open: %d", code)
	}
	if after := asOwner(t, snapshot); after != before {
		t.Fatalf("progress changed: %q -> %q", before, after)
	}
}

func TestOpenHintOnPassedTicketSkipsOrderIntegration(t *testing.T) {
	c, tok := ticketFixture(t)
	setProgress(t, "foundations/a", "passed", "")
	if code, body := openHint(c, tok, "foundations/a", "3"); code != 200 || !strings.Contains(body, "hint three") {
		t.Fatalf("passed: %d %q", code, body)
	}
}

func TestTicketHintErrorsIntegration(t *testing.T) {
	c, tok := ticketFixture(t)
	for _, tt := range []struct{ id, n string }{
		{"foundations/nope", "1"}, {"foundations/q", "1"}, {"foundations/b", "2"}, {"foundations/a", "4"}, {"foundations/a", "0"},
	} {
		if code, body := openHint(c, tok, tt.id, tt.n); code != 404 || strings.TrimSpace(body) != `{"error":"not_found"}` {
			t.Fatalf("%s/%s: %d %q", tt.id, tt.n, code, body)
		}
	}
	if code, body, _ := getAs(t, tok, "/v1/tickets/foundations/nope"); code != 404 || strings.TrimSpace(body) != `{"error":"not_found"}` {
		t.Fatalf("unknown read: %d %q", code, body)
	}
	if code, _, _ := getAs(t, tok, "/v1/tickets/foundations/q"); code != 404 {
		t.Fatalf("quiz read: %d", code)
	}
	if code, _ := openHint(c, "", "foundations/a", "1"); code != 401 {
		t.Fatalf("anonymous: %d", code)
	}
	asOwner(t, "UPDATE learners SET suspended_at = now() WHERE github_id = 1001")
	if code, body := openHint(c, tok, "foundations/a", "1"); code != 403 || strings.TrimSpace(body) != `{"error":"account_suspended"}` {
		t.Fatalf("suspended open: %d %q", code, body)
	}
	if code, body, _ := getAs(t, tok, "/v1/tickets/foundations/a"); code != 403 || strings.TrimSpace(body) != `{"error":"account_suspended"}` {
		t.Fatalf("suspended read: %d %q", code, body)
	}
	if n := asOwner(t, hintEvents); n != "0" {
		t.Fatalf("errors wrote events: %q", n)
	}
}

func TestOpenHintWithCookieNeedsCSRFIntegration(t *testing.T) {
	ticketFixture(t)
	ada := newBrowser(t)
	if resp := ada.signIn("ada", "", "age=on&terms=on", false); resp.Header.Get("Location") != "/app" {
		t.Fatalf("sign-in: %q", resp.Header.Get("Location"))
	}
	post := func(csrf bool) (int, string) {
		req, _ := http.NewRequest(http.MethodPost, apiURL(t)+"/v1/tickets/foundations/a/hints/1", bytes.NewReader([]byte("{}")))
		req.Header.Set("Content-Type", "application/json")
		if csrf {
			req.Header.Set(server.CSRFHeader, "1")
		}
		resp, err := ada.c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(out)
	}
	if code, _ := post(false); code != 403 {
		t.Fatalf("without CSRF: %d", code)
	}
	if n := asOwner(t, hintEvents); n != "0" {
		t.Fatalf("refused call wrote an event: %q", n)
	}
	if code, body := post(true); code != 200 || strings.TrimSpace(body) != `{"n":1,"text":"hint one"}` {
		t.Fatalf("with CSRF: %d %q", code, body)
	}
}

func TestTicketLimitsIntegration(t *testing.T) {
	c, tok := ticketFixture(t)
	for i := 0; i < 60; i++ {
		if code, body := openHint(c, tok, "foundations/a", "1"); code != 200 {
			t.Fatalf("hint %d: %d %s", i+1, code, body)
		}
	}
	if code, body := openHint(c, tok, "foundations/a", "1"); code != 429 || !strings.Contains(body, "rate_limited") {
		t.Fatalf("61st hint: %d %q", code, body)
	}
	for i := 0; i < 120; i++ {
		if code, _, _ := getAs(t, tok, "/v1/tickets/foundations/a"); code != 200 {
			t.Fatalf("read %d: %d", i+1, code)
		}
	}
	if code, _, h := getAs(t, tok, "/v1/tickets/foundations/a"); code != 429 || h.Get("Retry-After") == "" {
		t.Fatalf("121st read: %d %q", code, h.Get("Retry-After"))
	}
}
