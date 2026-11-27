package admin

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type searchCall struct{ Op, Field, Query, Reason string }

type fakeLearners struct {
	searches []searchCall
	views    []string
	found    []LearnerSummary
	more     bool
	view     LearnerView
	viewErr  error

	status    LearnerStatus
	statusErr error
	setErr    error
	sets      []setCall
	delErr    error
	deletes   []setCall
}

type setCall struct {
	Op, ID, Reason string
	Suspend        bool
}

func (f *fakeLearners) Status(context.Context, string) (LearnerStatus, error) {
	return f.status, f.statusErr
}

func (f *fakeLearners) SetSuspended(_ context.Context, op Operator, id string, suspend bool, reason string) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.sets = append(f.sets, setCall{op.Login, id, reason, suspend})
	return nil
}

func (f *fakeLearners) Delete(_ context.Context, op Operator, id, reason string) error {
	if f.delErr != nil {
		return f.delErr
	}
	f.deletes = append(f.deletes, setCall{Op: op.Login, ID: id, Reason: reason})
	return nil
}

func (f *fakeLearners) Search(_ context.Context, op Operator, field, query, reason string) ([]LearnerSummary, bool, error) {
	if _, err := checkSearch(field, query, reason); err != nil {
		return nil, false, err
	}
	f.searches = append(f.searches, searchCall{op.Login, field, query, reason})
	return f.found, f.more, nil
}

func (f *fakeLearners) View(_ context.Context, op Operator, id, _ string) (LearnerView, error) {
	f.views = append(f.views, op.Login+":"+id)
	return f.view, f.viewErr
}

const someID = "0192b7c8-0000-7000-8000-000000000001"

func TestLearnerRoutesNeedASession(t *testing.T) {
	for _, path := range []string{"/learners", "/learners?field=username&q=abc", "/learners/" + someID} {
		e := newEnv(t)
		wantRedirect(t, e.do(http.MethodGet, path), "/signin")
		wantRedirect(t, e.do(http.MethodGet, path, &http.Cookie{Name: sessionCookie, Value: "nope"}), "/signin")
		if len(e.learners.searches)+len(e.learners.views) != 0 {
			t.Fatalf("%s reached the store without a session", path)
		}
	}
}

func TestLearnerRoutesRefuseARevokedOperator(t *testing.T) {
	e := newEnv(t)
	s := signedIn(t, e)
	e.store.operators[1001].revoked = true
	wantRedirect(t, e.do(http.MethodGet, "/learners?field=username&q=abc", s), "/signin")
	wantRedirect(t, e.do(http.MethodGet, "/learners/"+someID, s), "/signin")
	if len(e.learners.searches)+len(e.learners.views) != 0 {
		t.Fatal("a revoked operator reached the store")
	}
}

func TestSearchFormNeedsNoAudit(t *testing.T) {
	e := newEnv(t)
	rec := e.do(http.MethodGet, "/learners", signedIn(t, e))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `name="q"`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if len(e.learners.searches) != 0 {
		t.Fatal("an empty form ran a search")
	}
}

func TestSearchPassesFieldQueryAndOperator(t *testing.T) {
	e := newEnv(t)
	e.learners.found = []LearnerSummary{{ID: someID, Username: "diego-daw", GithubLogin: "diegom-dev"}}
	s := signedIn(t, e)
	q := url.Values{"field": {"email"}, "q": {"Diego.M@example.com"}, "reason": {"ticket 12"}}
	rec := e.do(http.MethodGet, "/learners?"+q.Encode(), s)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	want := searchCall{"ada", "email", "Diego.M@example.com", "ticket 12"}
	if len(e.learners.searches) != 1 || e.learners.searches[0] != want {
		t.Fatalf("calls = %+v", e.learners.searches)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `href="/learners/`+someID+`?reason=ticket&#43;12"`) || !strings.Contains(body, "diego-daw") {
		t.Fatalf("results page: %s", body)
	}
}

func TestSearchRefusals(t *testing.T) {
	tests := []struct{ name, query string }{
		{"unknown field", "field=password&q=abcd"},
		{"no field", "q=abcd"},
		{"empty query", "field=username&q="},
		{"blank query", "field=email&q=+++"},
		{"one-character prefix", "field=username&q=a"},
		{"one-character login prefix", "field=github_login&q=a"},
		{"too long", "field=email&q=" + strings.Repeat("a", 255)},
		{"reason too long", "field=username&q=abc&reason=" + strings.Repeat("r", 1001)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			rec := e.do(http.MethodGet, "/learners?"+tt.query, signedIn(t, e))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d", rec.Code)
			}
			if len(e.learners.searches) != 0 {
				t.Fatal("a refused search was run")
			}
		})
	}
}

func TestSearchNoResultsAndMore(t *testing.T) {
	e := newEnv(t)
	s := signedIn(t, e)
	if body := e.do(http.MethodGet, "/learners?field=username&q=zzz", s).Body.String(); !strings.Contains(body, "No learner found.") {
		t.Fatalf("no-results page: %s", body)
	}
	e.learners.found, e.learners.more = []LearnerSummary{{ID: someID, Username: "abc"}}, true
	if body := e.do(http.MethodGet, "/learners?field=username&q=ab", s).Body.String(); !strings.Contains(body, "Narrow the search.") {
		t.Fatalf("more page: %s", body)
	}
}

func TestSearchEscapesWhatItEchoes(t *testing.T) {
	e := newEnv(t)
	body := e.do(http.MethodGet, "/learners?field=username&q="+url.QueryEscape(`"><script>x</script>`), signedIn(t, e)).Body.String()
	if strings.Contains(body, "<script>") {
		t.Fatalf("unescaped query: %s", body)
	}
}

func TestViewShowsTheLearner(t *testing.T) {
	e := newEnv(t)
	susp := time.Date(2026, 11, 2, 9, 30, 0, 0, time.UTC)
	e.learners.view = LearnerView{
		ID: someID, Username: "diego-daw", GithubLogin: "diegom-dev", Email: "diego.m@example.com", Language: "es",
		CreatedAt: time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC), SuspendedAt: &susp,
		Roles:  []RoleProgress{{Title: "Support L1", Passed: 2, Total: 5}},
		Recent: []TicketProgress{{ItemID: "support-l1/perms-03", Title: "Permissions", State: "started", Checks: 3, LastCheck: "1 of 4", LastSeenAt: susp}},
	}
	rec := e.do(http.MethodGet, "/learners/"+someID, signedIn(t, e))
	body := rec.Body.String()
	for _, want := range []string{"@diego-daw", "diegom-dev", "diego.m@example.com", "2 Oct 2026", "Suspended since 2 Nov 2026 09:30 UTC",
		"2 of 5", "support-l1/perms-03", "1 of 4", "Not available yet"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if len(e.learners.views) != 1 || e.learners.views[0] != "ada:"+someID {
		t.Fatalf("views = %v", e.learners.views)
	}
}

func TestViewUnknownLearnerIs404(t *testing.T) {
	e := newEnv(t)
	e.learners.viewErr = ErrNotFound
	if rec := e.do(http.MethodGet, "/learners/"+someID, signedIn(t, e)); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestViewStoreFailureIs500WithoutDetail(t *testing.T) {
	e := newEnv(t)
	e.learners.viewErr = errors.New("connection to db.internal refused")
	rec := e.do(http.MethodGet, "/learners/"+someID, signedIn(t, e))
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "db.internal") {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
}

func TestLearnerPagesRefuseWrites(t *testing.T) {
	e := newEnv(t)
	s := signedIn(t, e)
	for _, p := range []string{"/learners", "/learners/" + someID} {
		if rec := e.do(http.MethodPost, p, s); rec.Code == http.StatusOK || rec.Code == http.StatusSeeOther {
			t.Errorf("POST %s = %d", p, rec.Code)
		}
	}
}

func TestSearchPattern(t *testing.T) {
	tests := []struct{ field, query, want string }{
		{FieldUsername, "Diego", "diego%"},
		{FieldUsername, "a_b%c", `a\_b\%c%`},
		{FieldGithubLogin, `x\y`, `x\\y%`},
		{FieldEmail, "Diego.M@Example.com", "diego.m@example.com"},
		{FieldEmail, "a%b@x.io", "a%b@x.io"},
	}
	for _, tt := range tests {
		if got := pattern(tt.field, tt.query); got != tt.want {
			t.Errorf("pattern(%s, %q) = %q, want %q", tt.field, tt.query, got, tt.want)
		}
	}
}

func TestSecureHeadersCoverLearnerPages(t *testing.T) {
	e := newEnv(t)
	rec := e.do(http.MethodGet, "/learners/"+someID, signedIn(t, e))
	if rec.Header().Get("Cache-Control") != "no-store" || !strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'none'; style-src 'self'") {
		t.Fatalf("headers: %v", rec.Header())
	}
}
