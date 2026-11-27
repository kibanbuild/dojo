package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kibanyadev/dojo/internal/server/repo"
)

func TestParseTicketPath(t *testing.T) {
	tests := []struct {
		path  string
		id    string
		route ticketRoute
		n     int16
	}{
		{"/v1/tickets/support-l1/perms-03", "support-l1/perms-03", routeTicket, 0},
		{"/v1/tickets/support-l1/perms-03/bundle", "support-l1/perms-03", routeBundle, 0},
		{"/v1/tickets/support-l1/perms-03/hints/1", "support-l1/perms-03", routeHint, 1},
		{"/v1/tickets/support-l1/perms-03/hints/3", "support-l1/perms-03", routeHint, 3},
		{"/v1/tickets/support-l1/perms-03/hints/0", "", routeNone, 0},
		{"/v1/tickets/support-l1/perms-03/hints/4", "", routeNone, 0},
		{"/v1/tickets/support-l1/perms-03/hints/12", "", routeNone, 0},
		{"/v1/tickets/support-l1/perms-03/hints", "", routeNone, 0},
		{"/v1/tickets/support-l1/perms-03/other", "", routeNone, 0},
		{"/v1/tickets/support-l1/perms-03/bundle/x", "", routeNone, 0},
		{"/v1/tickets/perms-03", "", routeNone, 0},
		{"/v1/tickets/Bad_ID/x", "", routeNone, 0},
		{"/v1/tickets/", "", routeNone, 0},
		{"/v1/other/a/b", "", routeNone, 0},
	}
	for _, tt := range tests {
		id, route, n := parseTicketPath(tt.path)
		if id != tt.id || route != tt.route || n != tt.n {
			t.Errorf("%s: got (%q, %d, %d), want (%q, %d, %d)", tt.path, id, route, n, tt.id, tt.route, tt.n)
		}
	}
}

type fakeTickets struct {
	view     repo.GetTicketViewRow
	missing  bool
	progress *repo.TicketProgress
	opened   []int16
	err      error
	openErr  error
	openText string
	opens    []int16
}

func (f *fakeTickets) GetTicketView(_ context.Context, id string) (repo.GetTicketViewRow, error) {
	if f.err != nil {
		return repo.GetTicketViewRow{}, f.err
	}
	if f.missing || id != f.view.ID {
		return repo.GetTicketViewRow{}, pgx.ErrNoRows
	}
	return f.view, nil
}

func (f *fakeTickets) GetTicketProgress(context.Context, repo.GetTicketProgressParams) (repo.TicketProgress, error) {
	if f.progress == nil {
		return repo.TicketProgress{}, pgx.ErrNoRows
	}
	return *f.progress, nil
}

func (f *fakeTickets) ListHintOpens(context.Context, repo.ListHintOpensParams) ([]int16, error) {
	return f.opened, nil
}

func (f *fakeTickets) GetLearnerByID(context.Context, pgtype.UUID) (repo.Learner, error) {
	return repo.Learner{GithubLogin: "ada"}, nil
}

func (f *fakeTickets) OpenHint(_ context.Context, _ pgtype.UUID, _ string, n int16) (string, error) {
	f.opens = append(f.opens, n)
	return f.openText, f.openErr
}

func ticketFake() *fakeTickets {
	return &fakeTickets{view: repo.GetTicketViewRow{
		ID: "support-l1/perms-03", Role: pgtype.Text{String: "support-l1", Valid: true}, Title: "Fix the shared folder",
		EstMinutes: 20, Tags: []string{"permissions"}, Brief: pgtype.Text{String: "Ada <b>cannot</b> write", Valid: true},
		Hints: []string{"h1", "h2", "h3"},
	}, openText: "h2"}
}

func ticketCall(t *testing.T, f *fakeTickets, method, path string, suspend, anon, csrf bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if !anon {
		req.AddCookie(&http.Cookie{Name: SessionCookie, Value: goodToken})
	}
	if csrf {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(CSRFHeader, "1")
	}
	rec := httptest.NewRecorder()
	auth := Auth{Store: &fakeSessions{row: activeRow(time.Now(), suspend)}, Tickets: &Tickets{Reader: f, Opener: f}}
	New(fakeReady{}, auth, NewRateLimits(1), nil, nil).ServeHTTP(rec, req)
	return rec
}

func TestTicketRead(t *testing.T) {
	f := ticketFake()
	f.opened = []int16{1}
	rec := ticketCall(t, f, http.MethodGet, "/v1/tickets/support-l1/perms-03", false, false, false)
	var got struct {
		ID, Title, Role, Brief string
		EstMinutes             int `json:"est_minutes"`
		HintCount              int `json:"hint_count"`
		Hints                  []struct {
			N    int
			Text string
		}
		Progress *json.RawMessage
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != 200 {
		t.Fatalf("%d %v %s", rec.Code, err, rec.Body)
	}
	if got.ID != "support-l1/perms-03" || got.Role != "support-l1" || got.EstMinutes != 20 || got.HintCount != 3 ||
		got.Brief != "Ada <b>cannot</b> write" || len(got.Hints) != 1 || got.Hints[0].N != 1 || got.Hints[0].Text != "h1" || got.Progress != nil {
		t.Fatalf("body: %s", rec.Body)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("cache: %q", rec.Header().Get("Cache-Control"))
	}
}

func TestTicketReadHintsAreAnEmptyListNotNull(t *testing.T) {
	rec := ticketCall(t, ticketFake(), http.MethodGet, "/v1/tickets/support-l1/perms-03", false, false, false)
	if !strings.Contains(rec.Body.String(), `"hints":[]`) || !strings.Contains(rec.Body.String(), `"progress":null`) {
		t.Fatalf("body: %s", rec.Body)
	}
}

func TestTicketReadShowsAllHintsWhenPassed(t *testing.T) {
	for _, state := range []string{"passed", "revisit"} {
		f := ticketFake()
		f.progress = &repo.TicketProgress{State: state, CheckCount: 2,
			LastCheckOutput: pgtype.Text{String: "out", Valid: true},
			LastCheckAt:     pgtype.Timestamptz{Time: time.Date(2026, 11, 23, 10, 31, 40, 0, time.UTC), Valid: true}}
		rec := ticketCall(t, f, http.MethodGet, "/v1/tickets/support-l1/perms-03", false, false, false)
		var got struct {
			Hints    []struct{ N int }
			Progress map[string]any
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got.Hints) != 3 ||
			got.Progress["state"] != state || got.Progress["last_check_output"] != "out" || got.Progress["last_check_at"] != "2026-11-23T10:31:40Z" {
			t.Fatalf("%s: %v %s", state, err, rec.Body)
		}
	}
}

func TestTicketReadCommitState(t *testing.T) {
	sha := strings.Repeat("ab", 20)
	for _, tt := range []struct {
		state, wantState, wantURL string
	}{
		{"", "", ""},
		{"pending", "pending", ""},
		{"unverified", "unverified", ""},
		{"verified", "verified", "https://github.com/ada/dojo/commit/" + sha},
	} {
		f := ticketFake()
		f.progress = &repo.TicketProgress{State: "passed", PassedCommit: pgtype.Text{String: sha, Valid: tt.state != ""},
			CommitState: pgtype.Text{String: tt.state, Valid: tt.state != ""}}
		rec := ticketCall(t, f, http.MethodGet, "/v1/tickets/support-l1/perms-03", false, false, false)
		var got struct{ Progress map[string]any }
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		state, url := got.Progress["passed_commit_state"], got.Progress["passed_commit_url"]
		if (tt.wantState == "") != (state == nil) || (tt.wantState != "" && state != tt.wantState) ||
			(tt.wantURL == "") != (url == nil) || (tt.wantURL != "" && url != tt.wantURL) {
			t.Fatalf("%q: state %v url %v", tt.state, state, url)
		}
	}
}

func TestTicketErrors(t *testing.T) {
	tests := []struct {
		name                string
		method, path        string
		suspend, anon, csrf bool
		f                   func() *fakeTickets
		code                int
		body                string
	}{
		{"read anonymous", "GET", "/v1/tickets/support-l1/perms-03", false, true, false, ticketFake, 401, "unauthenticated"},
		{"read suspended", "GET", "/v1/tickets/support-l1/perms-03", true, false, false, ticketFake, 403, "account_suspended"},
		{"read unknown", "GET", "/v1/tickets/support-l1/nope", false, false, false, ticketFake, 404, "not_found"},
		{"read bad id", "GET", "/v1/tickets/Bad_ID/x", false, false, false, ticketFake, 404, "not_found"},
		{"read database", "GET", "/v1/tickets/support-l1/perms-03", false, false, false,
			func() *fakeTickets { f := ticketFake(); f.err = errors.New("down"); return f }, 500, "server_error"},
		{"bundle without store", "GET", "/v1/tickets/support-l1/perms-03/bundle", false, false, false, ticketFake, 404, "not_found"},
		{"hint anonymous", "POST", "/v1/tickets/support-l1/perms-03/hints/1", false, true, true, ticketFake, 401, "unauthenticated"},
		{"hint suspended", "POST", "/v1/tickets/support-l1/perms-03/hints/1", true, false, true, ticketFake, 403, "account_suspended"},
		{"hint no csrf", "POST", "/v1/tickets/support-l1/perms-03/hints/1", false, false, false, ticketFake, 403, "csrf_rejected"},
		{"hint out of range", "POST", "/v1/tickets/support-l1/perms-03/hints/4", false, false, true, ticketFake, 404, "not_found"},
		{"hint wrong path", "POST", "/v1/tickets/support-l1/perms-03", false, false, true, ticketFake, 404, "not_found"},
		{"hint unknown ticket", "POST", "/v1/tickets/support-l1/nope/hints/1", false, false, true,
			func() *fakeTickets { f := ticketFake(); f.openErr = repo.ErrTicketNotFound; return f }, 404, "not_found"},
		{"hint locked", "POST", "/v1/tickets/support-l1/perms-03/hints/3", false, false, true,
			func() *fakeTickets { f := ticketFake(); f.openErr = repo.ErrHintLocked; return f }, 409, "hint_locked"},
		{"hint learner gone", "POST", "/v1/tickets/support-l1/perms-03/hints/1", false, false, true,
			func() *fakeTickets { f := ticketFake(); f.openErr = repo.ErrLearnerGone; return f }, 401, "unauthenticated"},
		{"hint database", "POST", "/v1/tickets/support-l1/perms-03/hints/1", false, false, true,
			func() *fakeTickets { f := ticketFake(); f.openErr = errors.New("down"); return f }, 500, "server_error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := tt.f()
			rec := ticketCall(t, f, tt.method, tt.path, tt.suspend, tt.anon, tt.csrf)
			if rec.Code != tt.code || !strings.Contains(rec.Body.String(), `"`+tt.body+`"`) {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			if tt.name == "hint anonymous" || tt.name == "hint suspended" || tt.name == "hint no csrf" ||
				tt.name == "hint out of range" || tt.name == "hint wrong path" {
				if len(f.opens) != 0 {
					t.Fatalf("opened on %s", tt.name)
				}
			}
		})
	}
}

func TestOpenHintAnswers(t *testing.T) {
	f := ticketFake()
	rec := ticketCall(t, f, http.MethodPost, "/v1/tickets/support-l1/perms-03/hints/2", false, false, true)
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"n":2,"text":"h2"}` || len(f.opens) != 1 || f.opens[0] != 2 {
		t.Fatalf("%d %s %v", rec.Code, rec.Body, f.opens)
	}
}

func TestTicketRouteWithoutStore(t *testing.T) {
	for _, m := range []string{http.MethodGet, http.MethodPost} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(m, "/v1/tickets/support-l1/perms-03", nil)
		New(fakeReady{}, Auth{Store: &fakeSessions{}}, NewRateLimits(1), nil, nil).ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s: %d", m, rec.Code)
		}
	}
}

func TestTicketLimits(t *testing.T) {
	f := ticketFake()
	b := bundleFake()
	auth := Auth{Store: &fakeSessions{row: activeRow(time.Now(), false)}, Tickets: &Tickets{Reader: f, Opener: f},
		Bundles: &Bundles{Store: b, Signer: b}, Catalog: populatedCatalog()}
	h := New(fakeReady{}, auth, NewRateLimits(1), nil, nil)
	call := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.AddCookie(&http.Cookie{Name: SessionCookie, Value: goodToken})
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(CSRFHeader, "1")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	// The ticket read shares the 120 read budget with the catalog; a bundle fetch does not spend it.
	for i := 0; i < readBurst-1; i++ {
		if rec := call(http.MethodGet, "/v1/tickets/support-l1/perms-03"); rec.Code != 200 {
			t.Fatalf("read %d: %d", i, rec.Code)
		}
	}
	if rec := call(http.MethodGet, "/v1/catalog"); rec.Code != 200 {
		t.Fatalf("last read: %d", rec.Code)
	}
	for _, p := range []string{"/v1/tickets/support-l1/perms-03", "/v1/path"} {
		if rec := call(http.MethodGet, p); rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
			t.Fatalf("%s over budget: %d", p, rec.Code)
		}
	}
	if rec := call(http.MethodGet, "/v1/tickets/support-l1/perms-03/bundle"); rec.Code != 200 {
		t.Fatalf("bundle shares the read budget: %d", rec.Code)
	}
	// Hints share the events budget (60), not the read one.
	for i := 0; i < eventsBurst; i++ {
		if rec := call(http.MethodPost, "/v1/tickets/support-l1/perms-03/hints/1"); rec.Code != 200 {
			t.Fatalf("hint %d: %d", i, rec.Code)
		}
	}
	if rec := call(http.MethodPost, "/v1/tickets/support-l1/perms-03/hints/1"); rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("hint over budget: %d", rec.Code)
	}
}
