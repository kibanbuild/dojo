package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kibanbuild/dojo/internal/github"
	"github.com/kibanbuild/dojo/internal/server/repo"
)

type fakeOAuth struct {
	token       github.Token
	exchangeErr error
	revokeErr   error
	revokeFails int // transient revoke failures before it works
	exchanges   int
	emailsErr   error
	revoked     []string
	emailsCalls int
}

func (f *fakeOAuth) AuthorizeURL(state, ch, redirect string) string {
	return "https://gh.test/authorize?" + url.Values{"state": {state}, "code_challenge": {ch}, "redirect_uri": {redirect}}.Encode()
}

func (f *fakeOAuth) Exchange(context.Context, string, string, string) (github.Token, error) {
	f.exchanges++
	return f.token, f.exchangeErr
}

func (f *fakeOAuth) User(context.Context, string) (github.User, error) {
	return github.User{ID: 1001, Login: "Ada"}, nil
}

func (f *fakeOAuth) Emails(context.Context, string) (string, error) {
	f.emailsCalls++
	return "ada@example.test", f.emailsErr
}

func (f *fakeOAuth) Revoke(_ context.Context, tok string) error {
	f.revoked = append(f.revoked, tok)
	if f.revokeFails > 0 {
		f.revokeFails--
		return github.ErrUnavailable
	}
	return f.revokeErr
}

type fakeStore struct {
	known      *repo.Learner
	upserts    []repo.UpsertLearnerParams
	conflicts  int // unique violations before the insert works
	sessions   int
	sessionErr error

	revokedSessions []pgtype.UUID
}

func (f *fakeStore) GetOrgBySlug(context.Context, string) (repo.Org, error) {
	return repo.Org{ID: pgtype.UUID{Bytes: [16]byte{9}, Valid: true}}, nil
}

func (f *fakeStore) GetLearnerByGithubID(context.Context, int64) (repo.Learner, error) {
	if f.known == nil {
		return repo.Learner{}, pgx.ErrNoRows
	}
	return *f.known, nil
}

func (f *fakeStore) UpsertLearner(_ context.Context, a repo.UpsertLearnerParams) (repo.Learner, error) {
	f.upserts = append(f.upserts, a)
	if f.conflicts > 0 {
		f.conflicts--
		return repo.Learner{}, &pgconn.PgError{Code: "23505"}
	}
	return repo.Learner{ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, OrgID: a.OrgID, Username: a.Username}, nil
}

func (f *fakeStore) InsertSession(context.Context, repo.InsertSessionParams) (repo.InsertSessionRow, error) {
	f.sessions++
	return repo.InsertSessionRow{}, f.sessionErr
}

func (f *fakeStore) RevokeSession(_ context.Context, id pgtype.UUID) error {
	f.revokedSessions = append(f.revokedSessions, id)
	return nil
}

type rig struct {
	si *SignIn
	gh *fakeOAuth
	st *fakeStore
	h  http.Handler
	t0 time.Time
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{gh: &fakeOAuth{token: github.Token{AccessToken: "gho_secret", Scopes: []string{"read:user", "user:email"}}},
		st: &fakeStore{}, t0: time.Unix(1_800_000_000, 0)}
	r.si = &SignIn{GitHub: r.gh, Store: r.st, Origin: "http://localhost:8080", Key: []byte(DevStateKey), TermsVersion: "2026-11-20", Now: func() time.Time { return r.t0 },
		Backoff: []time.Duration{time.Millisecond, time.Millisecond}}
	r.h = New(fakeReady{}, Auth{Store: &fakeSessions{}}, NewRateLimits(1), r.si)
	return r
}

// begin posts the start form and returns the state cookie and the OAuth state.
func (r *rig) begin(t *testing.T, target, body string) (*http.Cookie, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	loc, _ := url.Parse(rec.Header().Get("Location"))
	cs := rec.Result().Cookies()
	if rec.Code != http.StatusSeeOther || len(cs) != 1 || loc.Query().Get("state") == "" {
		t.Fatalf("start: %d %v %q", rec.Code, cs, rec.Header().Get("Location"))
	}
	return cs[0], loc.Query().Get("state")
}

func (r *rig) callback(cookie *http.Cookie, query string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, callbackPath+"?"+query, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	return rec
}

const bothTicks = "age=on&terms=on"

func TestStartCookieAttributes(t *testing.T) {
	r := newRig(t)
	c, _ := r.begin(t, startPath, bothTicks)
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != statePath || c.MaxAge != 600 || c.Secure {
		t.Fatalf("cookie: %+v", c)
	}
	r.si.Auth.Secure = true
	if c, _ := r.begin(t, startPath, bothTicks); !c.Secure {
		t.Fatal("Secure missing for an https origin")
	}
}

func TestTicksComeFromTheBodyOnly(t *testing.T) {
	tests := []struct {
		name, target, body string
		age, terms         bool
	}{
		{"body ticks", startPath, bothTicks, true, true},
		{"query ticks ignored", startPath + "?" + bothTicks, "", false, false},
		{"one tick", startPath, "age=on", true, false},
		{"unticked value", startPath, "age=off&terms=", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			c, _ := r.begin(t, tt.target, tt.body)
			st, ok := r.si.open(c.Value)
			if !ok || st.Age != tt.age || st.Terms != tt.terms {
				t.Fatalf("state: %+v ok=%v", st, ok)
			}
		})
	}
}

func TestCallbackStateChecks(t *testing.T) {
	tamper := func(c *http.Cookie) *http.Cookie { d := *c; d.Value = "x" + c.Value[1:]; return &d }
	tests := []struct {
		name   string
		cookie func(c *http.Cookie) *http.Cookie
		state  func(s string) string
		age    time.Duration
	}{
		{"missing cookie", func(*http.Cookie) *http.Cookie { return nil }, func(s string) string { return s }, 0},
		{"tampered cookie", tamper, func(s string) string { return s }, 0},
		{"mismatched state", func(c *http.Cookie) *http.Cookie { return c }, func(string) string { return "other" }, 0},
		{"empty state", func(c *http.Cookie) *http.Cookie { return c }, func(string) string { return "" }, 0},
		{"expired", func(c *http.Cookie) *http.Cookie { return c }, func(s string) string { return s }, stateTTL + time.Second},
		{"from the future", func(c *http.Cookie) *http.Cookie { return c }, func(s string) string { return s }, -2 * stateSkew},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			c, state := r.begin(t, startPath, bothTicks)
			r.t0 = r.t0.Add(tt.age)
			rec := r.callback(tt.cookie(c), url.Values{"code": {"c"}, "state": {tt.state(state)}}.Encode())
			if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/app/signin?error=state_check" {
				t.Fatalf("got %d %q", rec.Code, rec.Header().Get("Location"))
			}
			if r.st.sessions != 0 || len(r.st.upserts) != 0 || len(r.gh.revoked) != 0 {
				t.Fatalf("state check let work through: %+v", r.st)
			}
		})
	}
}

func TestCallbackWrongKeyIsRejected(t *testing.T) {
	r := newRig(t)
	c, state := r.begin(t, startPath, bothTicks)
	r.si.Key = []byte("another-key-another-key-another-key-123")
	if rec := r.callback(c, "code=c&state="+state); rec.Header().Get("Location") != "/app/signin?error=state_check" {
		t.Fatalf("got %q", rec.Header().Get("Location"))
	}
}

func TestCallbackSuccessAndErrors(t *testing.T) {
	suspended := &repo.Learner{ID: pgtype.UUID{Valid: true}, Username: "ada", SuspendedAt: pgtype.Timestamptz{Valid: true}}
	tests := []struct {
		name     string
		setup    func(r *rig)
		query    string
		ticks    string
		want     string
		session  bool
		revoked  int
		upserts  int
		wantMail string
	}{
		{"new learner with ticks", func(*rig) {}, "code=c", bothTicks, "/app", true, 1, 1, "ada@example.test"},
		{"new learner without ticks", func(*rig) {}, "code=c", "", "/app/signin?confirm=1", false, 1, 0, ""},
		{"one tick is not enough", func(*rig) {}, "code=c", "age=on", "/app/signin?confirm=1", false, 1, 0, ""},
		{"known learner needs no ticks", func(r *rig) { r.st.known = &repo.Learner{Username: "ada"} }, "code=c", "", "/app", true, 1, 1, "ada@example.test"},
		{"suspended learner", func(r *rig) { r.st.known = suspended }, "code=c", bothTicks, "/app/signin?error=suspended", false, 1, 0, ""},
		{"user denied", func(*rig) {}, "error=access_denied&error_description=secret+text", bothTicks, "/app/signin?error=access_denied", false, 0, 0, ""},
		{"other github error", func(*rig) {}, "error=redirect_uri_mismatch", bothTicks, "/app/signin?error=github_refused", false, 0, 0, ""},
		{"no code", func(*rig) {}, "", bothTicks, "/app/signin?error=github_refused", false, 0, 0, ""},
		{"exchange rejected", func(r *rig) { r.gh.exchangeErr = github.ErrRejected }, "code=c", bothTicks, "/app/signin?error=github_refused", false, 0, 0, ""},
		{"exchange unavailable", func(r *rig) { r.gh.exchangeErr = github.ErrUnavailable }, "code=c", bothTicks, "/app/signin?error=github_unavailable", false, 0, 0, ""},
		{"revoke failure fails sign-in", func(r *rig) { r.gh.revokeErr = github.ErrUnavailable }, "code=c", bothTicks, "/app/signin?error=github_unavailable", false, 3, 0, ""},
		{"revoke succeeds on the last retry", func(r *rig) { r.gh.revokeFails = 2 }, "code=c", bothTicks, "/app", true, 3, 1, "ada@example.test"},
		{"emails failure still revokes", func(r *rig) { r.gh.emailsErr = github.ErrUnavailable }, "code=c", bothTicks, "/app/signin?error=github_unavailable", false, 1, 0, ""},
		{"no user:email grant signs in without email", func(r *rig) { r.gh.token.Scopes = []string{"read:user"} }, "code=c", bothTicks, "/app", true, 1, 1, ""},
		{"session insert fails", func(r *rig) { r.st.sessionErr = errors.New("db down") }, "code=c", bothTicks, "/app/signin?error=server_error", false, 1, 1, "ada@example.test"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			tt.setup(r)
			c, state := r.begin(t, startPath, tt.ticks)
			rec := r.callback(c, "state="+state+"&"+tt.query)
			if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != tt.want {
				t.Fatalf("got %d %q, want %q", rec.Code, rec.Header().Get("Location"), tt.want)
			}
			sess := false
			cleared := false
			for _, c := range rec.Result().Cookies() {
				sess = sess || (c.Name == SessionCookie && len(c.Value) == tokenLen)
				cleared = cleared || (c.Name == r.si.stateName() && c.MaxAge < 0)
			}
			if sess != tt.session || !cleared {
				t.Fatalf("session cookie=%v state cleared=%v", sess, cleared)
			}
			if len(r.gh.revoked) != tt.revoked || len(r.st.upserts) != tt.upserts {
				t.Fatalf("revoked=%d upserts=%d", len(r.gh.revoked), len(r.st.upserts))
			}
			if tt.upserts > 0 && r.st.upserts[0].Email.String != tt.wantMail {
				t.Fatalf("email %q", r.st.upserts[0].Email.String)
			}
			if strings.Contains(rec.Header().Get("Location"), "secret") {
				t.Fatal("GitHub text echoed")
			}
		})
	}
}

func TestNewLearnerRecordsServerClock(t *testing.T) {
	r := newRig(t)
	c, state := r.begin(t, startPath, bothTicks)
	r.callback(c, "code=c&state="+state)
	u := r.st.upserts[0]
	if !u.AgeConfirmedAt.Time.Equal(r.t0) || !u.TermsAcceptedAt.Time.Equal(r.t0) || u.Username != "ada" || u.GithubLogin != "Ada" {
		t.Fatalf("%+v", u)
	}
}

func TestNewLearnerStoresConfiguredTermsVersion(t *testing.T) {
	r := newRig(t)
	c, state := r.begin(t, startPath, bothTicks)
	r.callback(c, "code=c&state="+state)
	if got := r.st.upserts[0].TermsVersion; got != "2026-11-20" {
		t.Fatalf("terms version %q", got)
	}
}

func TestReturningLearnerKeepsItsTermsVersion(t *testing.T) {
	r := newRig(t)
	r.st.known = &repo.Learner{Username: "ada", TermsVersion: "2025-01-01"}
	c, state := r.begin(t, startPath, "")
	r.callback(c, "code=c&state="+state)
	if got := r.st.upserts[0].TermsVersion; got != "2025-01-01" {
		t.Fatalf("terms version %q", got)
	}
}

func TestParseTermsVersion(t *testing.T) {
	tests := []struct {
		name, in string
		ok       bool
	}{
		{"missing", "", false},
		{"date", "2026-11-20", true},
		{"semver", "v1.2.0", true},
		{"upper case", "V1", false},
		{"space", "2026 11", false},
		{"newline", "v1\n", false},
		{"32 chars", strings.Repeat("a", 32), true},
		{"33 chars", strings.Repeat("a", 33), false},
		{"reserved backfill marker", "unrecorded", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseTermsVersion(tt.in)
			if (err == nil) != tt.ok || (tt.ok && got != tt.in) {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
}

func TestUsernameRetriesOnUniqueViolation(t *testing.T) {
	r := newRig(t)
	r.st.conflicts = 2
	c, state := r.begin(t, startPath, bothTicks)
	rec := r.callback(c, "code=c&state="+state)
	if rec.Header().Get("Location") != "/app" || len(r.st.upserts) != 3 {
		t.Fatalf("got %q after %d inserts", rec.Header().Get("Location"), len(r.st.upserts))
	}
	for _, u := range r.st.upserts[1:] {
		if !usernameRE.MatchString(u.Username) || !strings.HasPrefix(u.Username, "ada-") {
			t.Fatalf("fallback %q", u.Username)
		}
	}
	r = newRig(t)
	r.st.conflicts = usernameTries
	c, state = r.begin(t, startPath, bothTicks)
	if rec := r.callback(c, "code=c&state="+state); rec.Header().Get("Location") != "/app/signin?error=server_error" || r.st.sessions != 0 {
		t.Fatalf("got %q", rec.Header().Get("Location"))
	}
}

func TestSignedInBrowserCanStart(t *testing.T) {
	r := newRig(t)
	f := &fakeSessions{row: activeRow(time.Now(), false)}
	r.h = New(fakeReady{}, Auth{Store: f}, NewRateLimits(1), r.si)
	req := httptest.NewRequest(http.MethodPost, startPath, strings.NewReader(bothTicks))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: goodToken})
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestStartRefusesCrossSiteRequests(t *testing.T) {
	tests := []struct {
		name, site, origin string
		refused            bool
	}{
		{"no headers", "", "", false},
		{"same origin", "same-origin", "http://localhost:8080", false},
		{"typed address", "none", "", false},
		{"cross-site fetch", "cross-site", "", true},
		{"sibling subdomain", "same-site", "", true},
		{"foreign origin only", "", "https://evil.example", true},
		{"null origin", "", "null", true},
		{"both", "cross-site", "https://evil.example", true},
		{"origin with another port", "", "http://localhost:9999", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			req := httptest.NewRequest(http.MethodPost, startPath, strings.NewReader(bothTicks))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if tt.site != "" {
				req.Header.Set("Sec-Fetch-Site", tt.site)
			}
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			rec := httptest.NewRecorder()
			r.h.ServeHTTP(rec, req)
			loc := rec.Header().Get("Location")
			if refused := loc == "/app/signin?error=state_check"; refused != tt.refused {
				t.Fatalf("got %d %q", rec.Code, loc)
			}
			if tt.refused && len(rec.Result().Cookies()) != 0 {
				t.Fatal("a refused start set a cookie")
			}
		})
	}
}

func TestStartLimitRedirectsAndCallbackIsNotLimited(t *testing.T) {
	r := newRig(t)
	r.h = New(fakeReady{}, Auth{Store: &fakeSessions{}}, testLimits(2), r.si)
	post := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, startPath, strings.NewReader(bothTicks))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = "1.1.1.1:1"
		rec := httptest.NewRecorder()
		r.h.ServeHTTP(rec, req)
		return rec
	}
	post()
	post()
	rec := post()
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/app/signin?error=rate_limited" || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("limited start: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	get := func(target string) int {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.RemoteAddr = "1.1.1.1:1"
		rec := httptest.NewRecorder()
		r.h.ServeHTTP(rec, req)
		return rec.Code
	}
	for i := 0; i < 5; i++ {
		if code := get(callbackPath + "?state=x"); code != http.StatusSeeOther {
			t.Fatalf("callback %d answered %d", i, code)
		}
	}
	// Only the exact route is exempt: variants of the path stay under the limit.
	if code := get(callbackPath + "/"); code != http.StatusTooManyRequests {
		t.Fatalf("callback variant answered %d, want 429", code)
	}
}

func TestStateIsSingleUse(t *testing.T) {
	r := newRig(t)
	c, state := r.begin(t, startPath, bothTicks)
	var locs []string
	for i := 0; i < 50; i++ {
		locs = append(locs, r.callback(c, "code=c&state="+state).Header().Get("Location"))
	}
	if locs[0] != "/app" {
		t.Fatalf("first use: %q", locs[0])
	}
	for i, l := range locs[1:] {
		if l != "/app/signin?error=state_check" {
			t.Fatalf("replay %d: %q", i+1, l)
		}
	}
	if r.gh.exchanges != 1 || r.st.sessions != 1 || len(r.gh.revoked) != 1 {
		t.Fatalf("outbound exchanges=%d revokes=%d sessions=%d, want 1 each", r.gh.exchanges, len(r.gh.revoked), r.st.sessions)
	}
}

func TestSpentStatesAreBoundedAndExpire(t *testing.T) {
	var s spentStates
	now := time.Unix(1000, 0)
	for i := 0; i < maxSpent; i++ {
		// Entry i expires i seconds after a minute: "0" is the oldest.
		if !s.take(strconv.Itoa(i), now, now.Add(time.Minute+time.Duration(i)*time.Second)) {
			t.Fatalf("state %d refused early", i)
		}
	}
	if !s.take("extra", now, now.Add(time.Hour)) {
		t.Fatal("a full table of live entries refused a new state")
	}
	if len(s.m) != maxSpent {
		t.Fatalf("table size %d, cap %d", len(s.m), maxSpent)
	}
	if _, ok := s.m["0"]; ok {
		t.Fatal("the oldest entry was not the one evicted")
	}
	if _, ok := s.m["1"]; !ok {
		t.Fatal("a newer entry was evicted")
	}
	if s.take("extra", now, now.Add(time.Hour)) {
		t.Fatal("the admitted state was not recorded as spent")
	}
	// Expired entries go first, before any live one is evicted.
	later := now.Add(time.Minute + 10*time.Second)
	if !s.take("fresh", later, later.Add(time.Hour)) {
		t.Fatal("a new state was refused")
	}
	if _, ok := s.m["20000"]; !ok || len(s.m) > maxSpent {
		t.Fatalf("a live entry was evicted while expired ones existed (size %d)", len(s.m))
	}
}

func TestCallbackLimiterCapsDistinctValidCookies(t *testing.T) {
	r := newRig(t)
	lim := testLimits(100)
	lim.Callback = newMemLimiter(2, time.Minute, 100, time.Now)
	r.h = New(fakeReady{}, Auth{Store: &fakeSessions{}}, lim, r.si)
	var locs []string
	for i := 0; i < 4; i++ {
		c, state := r.begin(t, startPath, bothTicks)
		locs = append(locs, r.callback(c, "code=c&state="+state).Header().Get("Location"))
	}
	want := []string{"/app", "/app", "/app/signin?error=rate_limited", "/app/signin?error=rate_limited"}
	for i := range want {
		if locs[i] != want[i] {
			t.Fatalf("got %q, want %q", locs, want)
		}
	}
	if r.gh.exchanges != 2 {
		t.Fatalf("%d outbound exchanges, want 2", r.gh.exchanges)
	}
	// Forged and missing cookies cost no token and no call.
	for i := 0; i < 5; i++ {
		r.callback(nil, "code=c&state=x")
	}
	if r.gh.exchanges != 2 {
		t.Fatalf("%d outbound exchanges after forged cookies", r.gh.exchanges)
	}
}

func TestCallbackExemptionGuards(t *testing.T) {
	tests := []struct{ name, method, target string }{
		{"doubled slash", "GET", "//v1/auth/github/callback"},
		{"trailing slash", "GET", callbackPath + "/"},
		{"dot segments", "GET", "/v1/auth/../auth/github/callback"},
		{"other case", "GET", "/V1/AUTH/github/callback"},
		{"POST method", "POST", callbackPath},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			r.h = New(fakeReady{}, Auth{Store: &fakeSessions{}}, testLimits(2), r.si)
			var codes []int
			for i := 0; i < 3; i++ {
				req := httptest.NewRequest(tt.method, tt.target, nil)
				req.RemoteAddr = "1.1.1.1:1"
				rec := httptest.NewRecorder()
				r.h.ServeHTTP(rec, req)
				codes = append(codes, rec.Code)
			}
			if codes[2] != http.StatusTooManyRequests || codes[0] == http.StatusTooManyRequests {
				t.Fatalf("got %v, want the third request limited", codes)
			}
		})
	}
}

func TestSuccessfulCallbackRevokesThePresentedSession(t *testing.T) {
	r := newRig(t)
	f := &fakeSessions{row: activeRow(time.Now(), false)}
	f.row.ID = pgtype.UUID{Bytes: [16]byte{7}, Valid: true}
	r.h = New(fakeReady{}, Auth{Store: f}, NewRateLimits(1), r.si)
	run := func(withSession bool) {
		c, state := r.begin(t, startPath, bothTicks)
		req := httptest.NewRequest(http.MethodGet, callbackPath+"?code=c&state="+state, nil)
		req.AddCookie(c)
		if withSession {
			req.AddCookie(&http.Cookie{Name: SessionCookie, Value: goodToken})
		}
		rec := httptest.NewRecorder()
		r.h.ServeHTTP(rec, req)
		if rec.Header().Get("Location") != "/app" {
			t.Fatalf("got %q", rec.Header().Get("Location"))
		}
	}
	run(false)
	if len(r.st.revokedSessions) != 0 {
		t.Fatalf("revoked %v with no session presented", r.st.revokedSessions)
	}
	run(true)
	if len(r.st.revokedSessions) != 1 || r.st.revokedSessions[0] != f.row.ID {
		t.Fatalf("revoked %v, want the presented session", r.st.revokedSessions)
	}
}

func TestSecureOriginUsesThePrefixedStateCookie(t *testing.T) {
	r := newRig(t)
	r.si.Auth.Secure = true
	c, state := r.begin(t, startPath, bothTicks)
	if c.Name != "__Secure-dojo_oauth" || !c.Secure {
		t.Fatalf("cookie %q secure=%v", c.Name, c.Secure)
	}
	if rec := r.callback(c, "code=c&state="+state); rec.Header().Get("Location") != "/app" {
		t.Fatalf("got %q", rec.Header().Get("Location"))
	}
	plain := *c
	plain.Name = stateCookie
	if rec := r.callback(&plain, "code=c&state="+state); rec.Header().Get("Location") != "/app/signin?error=state_check" {
		t.Fatalf("an unprefixed cookie was accepted: %q", rec.Header().Get("Location"))
	}
}

func TestStateWithValidMACAndWrongVersionIsRejected(t *testing.T) {
	r := newRig(t)
	st := oauthState{V: stateVersion, State: "s", Verifier: "v", Issued: r.t0.Unix()}
	if _, ok := r.si.open(r.si.seal(st)); !ok {
		t.Fatal("the control payload was rejected")
	}
	for _, v := range []int{0, stateVersion + 1} {
		st.V = v
		if _, ok := r.si.open(r.si.seal(st)); ok {
			t.Fatalf("version %d accepted", v)
		}
	}
}

func TestUsernames(t *testing.T) {
	tests := []struct {
		login, want string
		ok          bool
	}{
		{"Ada", "ada", true},
		{"a", "a", false},
		{"ab", "ab", false},
		{"abc", "abc", true},
		{"admin", "admin", false},
		{"Admin", "admin", false},
		{"Kiban", "kiban", false},
		{strings.Repeat("a", 30), strings.Repeat("a", 30), true},
		{strings.Repeat("a", 31), strings.Repeat("a", 31), false},
		{strings.Repeat("A", 39), strings.Repeat("a", 39), false},
		{"-edge-", "-edge-", true},
	}
	for _, tt := range tests {
		got, ok := defaultUsername(tt.login)
		if got != tt.want || ok != tt.ok {
			t.Errorf("default(%q) = %q, %v", tt.login, got, ok)
		}
		if f := fallbackUsername(tt.login, "a1b2c3"); !usernameRE.MatchString(f) {
			t.Errorf("fallback(%q) = %q does not fit the CHECK", tt.login, f)
		}
	}
	for login, want := range map[string]string{
		"a": "a-a1b2c3", "Admin": "admin-a1b2c3", "": "learner-a1b2c3", "--": "learner-a1b2c3", "x_y.z": "xyz-a1b2c3",
		strings.Repeat("a", 39): strings.Repeat("a", 23) + "-a1b2c3",
	} {
		if got := fallbackUsername(login, "a1b2c3"); got != want {
			t.Errorf("fallback(%q) = %q, want %q", login, got, want)
		}
	}
	for name := range reservedUsernames {
		if strings.Contains(name, "-") {
			t.Errorf("reserved %q has a hyphen", name)
		}
	}
}

func TestParseStateKey(t *testing.T) {
	tests := []struct {
		name, key string
		local     bool
		ok        bool
	}{
		{"missing", "", true, false},
		{"short", strings.Repeat("ab", 15), true, false},
		{"repetitive", strings.Repeat("a", 40), true, false},
		{"dev value in local", DevStateKey, true, true},
		{"dev value in pro", DevStateKey, false, false},
		{"real key in pro", strings.Repeat("abcdefgh", 5), false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseStateKey(tt.key, tt.local); (err == nil) != tt.ok {
				t.Fatalf("err = %v", err)
			}
		})
	}
}
