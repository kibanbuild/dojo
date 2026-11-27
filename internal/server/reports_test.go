package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kibanyadev/dojo/internal/mail"
	"github.com/kibanyadev/dojo/internal/server/repo"
)

func TestReportValidation(t *testing.T) {
	item := func(s string) *string { return &s }
	tests := []struct {
		name string
		req  reportRequest
		want string
	}{
		{"broken ticket", reportRequest{Kind: "broken_ticket", ItemID: item("support-l1/perms-03"), Message: "It fails."}, ""},
		{"setup help without item", reportRequest{Kind: "setup_help", Message: "Lima will not start."}, ""},
		{"setup help with null and empty item", reportRequest{Kind: "setup_help", ItemID: item(""), Message: "x"}, ""},
		{"setup help with item", reportRequest{Kind: "setup_help", ItemID: item("foundations/a"), Message: "x"}, ""},
		{"message is trimmed to one character", reportRequest{Kind: "setup_help", Message: "  x \n"}, ""},
		{"2000 characters", reportRequest{Kind: "setup_help", Message: strings.Repeat("é", 2000)}, ""},
		{"2001 characters", reportRequest{Kind: "setup_help", Message: strings.Repeat("é", 2001)}, "bad_request"},
		{"empty message", reportRequest{Kind: "setup_help", Message: ""}, "bad_request"},
		{"blank message", reportRequest{Kind: "setup_help", Message: " \t\n"}, "bad_request"},
		{"NUL in message", reportRequest{Kind: "setup_help", Message: "a\x00b"}, "bad_request"},
		{"unknown kind", reportRequest{Kind: "other", Message: "x"}, "bad_request"},
		{"no kind", reportRequest{Message: "x"}, "bad_request"},
		{"broken ticket without item", reportRequest{Kind: "broken_ticket", Message: "x"}, "bad_request"},
		{"broken ticket with empty item", reportRequest{Kind: "broken_ticket", ItemID: item(""), Message: "x"}, "bad_request"},
		{"item that cannot be a ticket", reportRequest{Kind: "broken_ticket", ItemID: item("../etc"), Message: "x"}, "not_found"},
		{"item with a NUL", reportRequest{Kind: "setup_help", ItemID: item("a/b\x00"), Message: "x"}, "not_found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in, got := tt.req.validate()
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
			if tt.want == "" && (in.Message != strings.TrimSpace(tt.req.Message) || in.Kind != tt.req.Kind) {
				t.Fatalf("report: %+v", in)
			}
		})
	}
}

type fakeReports struct {
	got []repo.ReportIn
	res repo.ReportResult
	err error
}

func (f *fakeReports) CreateReport(_ context.Context, in repo.ReportIn) (repo.ReportResult, error) {
	f.got = append(f.got, in)
	return f.res, f.err
}

type reportCall struct {
	cookie, csrf bool
	ctype, body  string
	suspend      bool
}

func doReport(t *testing.T, store ReportStore, c reportCall) *httptest.ResponseRecorder {
	t.Helper()
	f := &fakeSessions{learner: repo.Learner{Username: "ada"}}
	req := httptest.NewRequest(http.MethodPost, reportsPath, strings.NewReader(c.body))
	req.Header.Set("Content-Type", "application/json")
	if c.ctype != "" {
		req.Header.Set("Content-Type", c.ctype)
	}
	if c.cookie {
		f.row = activeRow(time.Now(), c.suspend)
		req.AddCookie(&http.Cookie{Name: SessionCookie, Value: goodToken})
		if c.csrf {
			req.Header.Set(CSRFHeader, "1")
		}
	} else {
		f.cli = activeCLI(time.Now(), c.suspend)
		req.Header.Set("Authorization", "Bearer "+goodCLIToken)
	}
	rec := httptest.NewRecorder()
	New(fakeReady{}, Auth{Store: f, Reports: store}, NewRateLimits(1), nil, nil).ServeHTTP(rec, req)
	return rec
}

const goodReport = `{"kind":"broken_ticket","item_id":"support-l1/perms-03","message":" It fails. ","include_output":true}`

func TestPostReport(t *testing.T) {
	tests := []struct {
		name     string
		call     reportCall
		store    *fakeReports
		want     int
		wantBody string
		stored   bool
		retry    string
	}{
		{"bearer", reportCall{body: goodReport}, &fakeReports{res: repo.ReportResult{ID: uid(7)}}, 201, `"id":"07000000-`, true, ""},
		{"cookie with csrf", reportCall{cookie: true, csrf: true, body: goodReport}, &fakeReports{}, 201, `"id"`, true, ""},
		{"cookie without csrf", reportCall{cookie: true, body: goodReport}, &fakeReports{}, 403, "csrf_rejected", false, ""},
		{"suspended learner", reportCall{suspend: true, body: goodReport}, &fakeReports{}, 403, "account_suspended", false, ""},
		{"not json", reportCall{ctype: "text/plain", body: goodReport}, &fakeReports{}, 400, "bad_request", false, ""},
		{"unknown field", reportCall{body: `{"kind":"setup_help","message":"x","doctor_summary":"forged"}`}, &fakeReports{}, 400, "bad_request", false, ""},
		{"client output is refused", reportCall{body: `{"kind":"setup_help","message":"x","last_check_output":"forged"}`}, &fakeReports{}, 400, "bad_request", false, ""},
		{"trailing data", reportCall{body: goodReport + `{}`}, &fakeReports{}, 400, "bad_request", false, ""},
		{"not an object", reportCall{body: `[]`}, &fakeReports{}, 400, "bad_request", false, ""},
		{"over 8 KB", reportCall{body: `{"kind":"setup_help","message":"` + strings.Repeat("a", 9000) + `"}`}, &fakeReports{}, 400, "bad_request", false, ""},
		{"bad message", reportCall{body: `{"kind":"setup_help","message":""}`}, &fakeReports{}, 400, "bad_request", false, ""},
		{"missing item", reportCall{body: `{"kind":"broken_ticket","message":"x"}`}, &fakeReports{}, 400, "bad_request", false, ""},
		{"malformed item", reportCall{body: `{"kind":"broken_ticket","item_id":"x","message":"x"}`}, &fakeReports{}, 404, "not_found", false, ""},
		{"unknown item", reportCall{body: goodReport}, &fakeReports{err: repo.ErrUnknownItem}, 404, "not_found", true, ""},
		{"rate limited", reportCall{body: goodReport}, &fakeReports{res: repo.ReportResult{RetryAfter: 90*time.Second + time.Millisecond}}, 429, "rate_limited", true, "91"},
		{"store failure", reportCall{body: goodReport}, &fakeReports{err: errors.New("boom")}, 500, "server_error", true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := doReport(t, tt.store, tt.call)
			if rec.Code != tt.want || !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Fatalf("got %d %q", rec.Code, rec.Body.String())
			}
			if (len(tt.store.got) == 1) != tt.stored {
				t.Fatalf("store calls: %d", len(tt.store.got))
			}
			if got := rec.Header().Get("Retry-After"); got != tt.retry {
				t.Fatalf("Retry-After %q, want %q", got, tt.retry)
			}
			if tt.stored && tt.want == 201 {
				in := tt.store.got[0]
				if in.Message != "It fails." || !in.IncludeOutput || in.ItemID != "support-l1/perms-03" || !in.LearnerID.Valid {
					t.Fatalf("report: %+v", in)
				}
			}
		})
	}
}

func TestPostReportAnonymous(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, reportsPath, strings.NewReader(goodReport))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	New(fakeReady{}, Auth{Store: &fakeSessions{}, Reports: &fakeReports{}}, NewRateLimits(1), nil, nil).ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("got %d", rec.Code)
	}
}

type fakeReportQueue struct {
	reports []repo.Report
	done    map[[16]byte]bool
	skipped [][]pgtype.UUID
}

func (q *fakeReportQueue) ForwardReport(ctx context.Context, skip []pgtype.UUID, send func(context.Context, repo.Report) error) (pgtype.UUID, bool, error) {
	q.skipped = append(q.skipped, skip)
next:
	for _, r := range q.reports {
		if q.done[r.ID.Bytes] {
			continue
		}
		for _, s := range skip {
			if s == r.ID {
				continue next
			}
		}
		if err := send(ctx, r); err != nil {
			return r.ID, true, err
		}
		if q.done == nil {
			q.done = map[[16]byte]bool{}
		}
		q.done[r.ID.Bytes] = true
		return r.ID, true, nil
	}
	return pgtype.UUID{}, false, nil
}

func sampleReport(n byte) repo.Report {
	return repo.Report{ID: uid(n), Kind: "broken_ticket", ItemID: "support-l1/perms-03", CLIVersion: "v0.4.1",
		Output: "FAIL owner\n```\nnested\n```\n", Message: "It fails.", DoctorSummary: "lima: not installed",
		GithubLogin: "ada", Email: "ada@example.test"}
}

func TestReportMailContent(t *testing.T) {
	q := &fakeReportQueue{reports: []repo.Report{sampleReport(1)}}
	m := &fakeMail{}
	f := &ReportForwarder{Queue: q, Mail: m, To: "reports@dojo.test", Domain: "dojo.test"}
	if n, err := f.Sweep(context.Background()); err != nil || n != 1 {
		t.Fatalf("sent %d, %v", n, err)
	}
	got := m.sent[0]
	if got.To != "reports@dojo.test" || got.From != "hello@dojo.test" || got.ReplyTo != "ada@example.test" ||
		got.Subject != "[dojo report] broken_ticket support-l1/perms-03 00000000" {
		t.Fatalf("message: %+v", got)
	}
	for _, want := range []string{"Message:\nIt fails.", "Learner: ada", "CLI version: v0.4.1", "Doctor summary:\nlima: not installed",
		"````\nFAIL owner\n```\nnested\n```\n````"} {
		if !strings.Contains(got.Text, want) {
			t.Errorf("text misses %q: %q", want, got.Text)
		}
	}
	if !strings.HasPrefix(got.HTML, "<pre>") || strings.Contains(got.HTML, "<script") {
		t.Fatalf("html: %q", got.HTML)
	}
	if _, err := mail.Build(got, time.Now(), "id"); err != nil {
		t.Fatalf("message does not build: %v", err)
	}
}

func TestReportMailWithoutLearnerFacts(t *testing.T) {
	r := repo.Report{ID: uid(2), Kind: "setup_help", Message: "Stuck.", GithubLogin: "ada", Email: "not an address"}
	m := &fakeMail{}
	f := &ReportForwarder{Queue: &fakeReportQueue{reports: []repo.Report{r}}, Mail: m, To: "reports@dojo.test", Domain: "dojo.test"}
	if _, err := f.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := m.sent[0]
	if got.ReplyTo != "" || got.Subject != "[dojo report] setup_help - 00000000" ||
		strings.Contains(got.Text, "Doctor summary") || strings.Contains(got.Text, "Last check output") {
		t.Fatalf("message: %+v %q", got, got.Text)
	}
}

type logBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func TestReportSendFailureIsRetriedLaterAndLogsNoText(t *testing.T) {
	var logs logBuf
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

	now := time.Now()
	q := &fakeReportQueue{reports: []repo.Report{sampleReport(1), sampleReport(2)}}
	m := &fakeMail{fail: map[string]error{"reports@dojo.test": &textproto.Error{Code: 451, Msg: "ada@example.test greylisted: It fails."}}}
	f := &ReportForwarder{Queue: q, Mail: m, To: "reports@dojo.test", Domain: "dojo.test", Now: func() time.Time { return now }}

	if n, err := f.Sweep(context.Background()); err != nil || n != 0 {
		t.Fatalf("first sweep: %d, %v", n, err)
	}
	if len(q.done) != 0 {
		t.Fatalf("a failed send marked a report: %v", q.done)
	}
	for _, bad := range []string{"ada@example.test", "It fails.", "reports@dojo.test", "greylisted", "support-l1"} {
		if strings.Contains(logs.b.String(), bad) {
			t.Errorf("log carries %q: %s", bad, logs.b.String())
		}
	}
	if !strings.Contains(logs.b.String(), "class=smtp") || !strings.Contains(logs.b.String(), "smtp_code=451") {
		t.Errorf("log lacks the failure class: %s", logs.b.String())
	}

	// Both failed rows back off, so the next sweep within the wait claims nothing; after it, the mail is retried.
	m.fail = nil
	if n, _ := f.Sweep(context.Background()); n != 0 {
		t.Fatalf("sweep within the backoff sent %d", n)
	}
	now = now.Add(transientBackoff + time.Second)
	if n, err := f.Sweep(context.Background()); err != nil || n != 2 {
		t.Fatalf("sweep after the backoff: %d, %v", n, err)
	}
}
