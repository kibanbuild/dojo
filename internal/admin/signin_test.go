package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kibanyadev/dojo/internal/github"
)

// memStore is a Store in memory. Operators are keyed by GitHub id.
type memStore struct {
	mu        sync.Mutex
	now       func() time.Time
	operators map[int64]*memOperator
	sessions  map[string]*memSession
	failOp    error
}

type memOperator struct {
	Operator
	revoked bool
}

type memSession struct {
	op      *memOperator
	expires time.Time
	revoked bool
}

func newMemStore(now func() time.Time) *memStore {
	return &memStore{now: now, operators: map[int64]*memOperator{}, sessions: map[string]*memSession{}}
}

func (m *memStore) addOperator(githubID int64, login string) *memOperator {
	o := &memOperator{Operator: Operator{ID: "op-" + login, Login: login}}
	m.operators[githubID] = o
	return o
}

func (m *memStore) OperatorByGithubID(_ context.Context, id int64) (Operator, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failOp != nil {
		return Operator{}, m.failOp
	}
	o, ok := m.operators[id]
	if !ok || o.revoked {
		return Operator{}, ErrNotFound
	}
	return o.Operator, nil
}

func (m *memStore) CreateSession(_ context.Context, operatorID string, hash []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, o := range m.operators {
		if o.ID == operatorID {
			if len(hash) != 32 {
				return errors.New("hash must be 32 bytes")
			}
			m.sessions[string(hash)] = &memSession{op: o, expires: m.now().Add(time.Hour)}
			return nil
		}
	}
	return errors.New("no such operator")
}

func (m *memStore) ActiveSession(_ context.Context, hash []byte) (Operator, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[string(hash)]
	if !ok || s.revoked || !s.expires.After(m.now()) || s.op.revoked {
		return Operator{}, ErrNotFound
	}
	return s.op.Operator, nil
}

func (m *memStore) RevokeSession(_ context.Context, hash []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[string(hash)]; ok {
		s.revoked = true
	}
	return nil
}

// ghStub is a GitHub that answers what the knobs say.
type ghStub struct {
	mu         sync.Mutex
	tokenBody  string // /login/oauth/access_token JSON
	userStatus int
	userBody   string
	scopes     string // X-OAuth-Scopes
	revokeCode int
	revoked    []string
	exchanges  int
	lastVerify string
}

func newStub() *ghStub {
	return &ghStub{
		tokenBody:  `{"access_token":"gho_tok","scope":"read:user","token_type":"bearer"}`,
		userStatus: 200,
		userBody:   `{"id":1001,"login":"ada","two_factor_authentication":true}`,
		scopes:     "read:user",
		revokeCode: 204,
	}
}

func (g *ghStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case r.URL.Path == "/login/oauth/access_token":
		g.exchanges++
		_ = r.ParseForm()
		g.lastVerify = r.PostForm.Get("code_verifier")
		_, _ = w.Write([]byte(g.tokenBody))
	case r.URL.Path == "/user":
		w.Header().Set("X-OAuth-Scopes", g.scopes)
		w.WriteHeader(g.userStatus)
		_, _ = w.Write([]byte(g.userBody))
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/applications/"):
		g.revoked = append(g.revoked, "gho_tok")
		w.WriteHeader(g.revokeCode)
	default:
		http.NotFound(w, r)
	}
}

type env struct {
	t     *testing.T
	h     http.Handler
	store *memStore
	gh    *ghStub
	now   time.Time
	mu    sync.Mutex

	learners *fakeLearners
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, gh: newStub(), now: time.Date(2026, 12, 1, 10, 0, 0, 0, time.UTC)}
	srv := httptest.NewServer(e.gh)
	t.Cleanup(srv.Close)
	clock := func() time.Time { e.mu.Lock(); defer e.mu.Unlock(); return e.now }
	e.store = newMemStore(clock)
	e.store.addOperator(1001, "ada")
	client := github.New(github.Config{ClientID: "dojo-admin-local", ClientSecret: "s", AuthorizeURL: "http://browser.test",
		OAuthURL: srv.URL, APIURL: srv.URL, Scopes: Scope})
	e.learners = &fakeLearners{}
	e.h = New(fakeReady{}, Deps{GitHub: client, Store: e.store, Learners: e.learners, Now: clock, Backoff: []time.Duration{time.Millisecond}})
	return e
}

func (e *env) advance(d time.Duration) { e.mu.Lock(); e.now = e.now.Add(d); e.mu.Unlock() }

func (e *env) do(method, target string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	req.Host = Host
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func cookieNamed(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// start runs the start step and returns the pre-session cookie and the state sent to GitHub.
func (e *env) start() (*http.Cookie, string) {
	e.t.Helper()
	rec := e.do(http.MethodPost, startPath)
	loc, err := url.Parse(rec.Header().Get("Location"))
	c := cookieNamed(rec, oauthCookie)
	if rec.Code != http.StatusSeeOther || err != nil || c == nil || loc.Host != "browser.test" || loc.Path != "/login/oauth/authorize" {
		e.t.Fatalf("start: %d %q %v", rec.Code, rec.Header().Get("Location"), c)
	}
	q := loc.Query()
	if q.Get("client_id") != "dojo-admin-local" || q.Get("scope") != "read:user" || q.Get("redirect_uri") != "http://127.0.0.1:8090/auth/github/callback" ||
		q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		e.t.Fatalf("authorize query: %v", q)
	}
	return c, q.Get("state")
}

func (e *env) callback(c *http.Cookie, state, extra string) *httptest.ResponseRecorder {
	q := "state=" + url.QueryEscape(state) + "&code=abc" + extra
	if c == nil {
		return e.do(http.MethodGet, callbackPath+"?"+q)
	}
	return e.do(http.MethodGet, callbackPath+"?"+q, c)
}

func wantRedirect(t *testing.T, rec *httptest.ResponseRecorder, loc string) {
	t.Helper()
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != loc {
		t.Fatalf("got %d %q, want 303 %q", rec.Code, rec.Header().Get("Location"), loc)
	}
	if c := cookieNamed(rec, sessionCookie); c != nil && c.Value != "" && strings.HasPrefix(loc, "/signin") {
		t.Fatalf("a refusal set a session cookie: %+v", c)
	}
}

func TestSigninPage(t *testing.T) {
	e := newEnv(t)
	rec := e.do(http.MethodGet, "/signin?error=two_factor")
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `action="/auth/github/start"`) || !strings.Contains(body, "two-factor") {
		t.Fatalf("%d %s", rec.Code, body)
	}
	// An unknown code shows nothing: the query is never echoed.
	rec = e.do(http.MethodGet, "/signin?error=%3Cscript%3E")
	if strings.Contains(rec.Body.String(), "script") || strings.Contains(rec.Body.String(), `role="alert"`) {
		t.Fatalf("echoed: %s", rec.Body.String())
	}
	for _, h := range []string{"Cache-Control", "Content-Security-Policy", "X-Frame-Options"} {
		if rec.Header().Get(h) == "" {
			t.Errorf("missing %s", h)
		}
	}
}

func TestStartCookieAttributes(t *testing.T) {
	e := newEnv(t)
	c, _ := e.start()
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/auth/github" || c.Domain != "" || c.MaxAge != 300 {
		t.Fatalf("%+v", c)
	}
}

func TestStartRefusesCrossSite(t *testing.T) {
	e := newEnv(t)
	req := httptest.NewRequest(http.MethodPost, startPath, nil)
	req.Host = Host
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Header().Get("Location") != "/signin?error=state_check" || cookieNamed(rec, oauthCookie) != nil {
		t.Fatalf("%d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestSignInSuccess(t *testing.T) {
	e := newEnv(t)
	c, state := e.start()
	rec := e.callback(c, state, "")
	wantRedirect(t, rec, "/")
	s := cookieNamed(rec, sessionCookie)
	if s == nil || s.Value == "" || !s.HttpOnly || s.SameSite != http.SameSiteLaxMode || s.Path != "/" || s.Domain != "" || s.Secure || s.MaxAge != 3600 {
		t.Fatalf("session cookie: %+v", s)
	}
	if len(s.Value) < 43 { // 32 random bytes in base64url
		t.Fatalf("token too short: %q", s.Value)
	}
	if len(e.store.sessions) != 1 {
		t.Fatalf("sessions: %d", len(e.store.sessions))
	}
	for k := range e.store.sessions {
		if k == s.Value || len(k) != 32 {
			t.Fatalf("stored the token itself or a wrong hash length")
		}
	}
	if len(e.gh.revoked) != 1 || e.gh.lastVerify == "" {
		t.Fatalf("revoked %v verifier %q", e.gh.revoked, e.gh.lastVerify)
	}
	if pc := cookieNamed(rec, oauthCookie); pc == nil || pc.MaxAge >= 0 {
		t.Fatalf("pre-session cookie not cleared: %+v", pc)
	}

	land := e.do(http.MethodGet, "/", s)
	if land.Code != 200 || !strings.Contains(land.Body.String(), "Signed in as ada") {
		t.Fatalf("landing: %d %s", land.Code, land.Body.String())
	}
}

func TestSignInUserScopeAlsoCounts(t *testing.T) {
	e := newEnv(t)
	e.gh.scopes = "user"
	c, state := e.start()
	wantRedirect(t, e.callback(c, state, ""), "/")
}

func TestSignInRefusals(t *testing.T) {
	tests := []struct {
		name  string
		setup func(e *env)
		want  string
	}{
		{"not an operator", func(e *env) { delete(e.store.operators, 1001) }, "/signin?error=not_authorized"},
		{"revoked operator", func(e *env) { e.store.operators[1001].revoked = true }, "/signin?error=not_authorized"},
		{"no 2fa", func(e *env) { e.gh.userBody = `{"id":1001,"login":"ada","two_factor_authentication":false}` }, "/signin?error=two_factor"},
		{"2fa field missing", func(e *env) { e.gh.userBody = `{"id":1001,"login":"ada"}` }, "/signin?error=two_factor"},
		{"2fa field null", func(e *env) { e.gh.userBody = `{"id":1001,"login":"ada","two_factor_authentication":null}` }, "/signin?error=two_factor"},
		{"2fa field a string", func(e *env) { e.gh.userBody = `{"id":1001,"login":"ada","two_factor_authentication":"true"}` }, "/signin?error=github_unavailable"},
		{"scope not granted", func(e *env) { e.gh.scopes = "user:email" }, "/signin?error=two_factor"},
		{"no scopes header", func(e *env) { e.gh.scopes = "" }, "/signin?error=two_factor"},
		{"user call fails", func(e *env) { e.gh.userStatus = 502 }, "/signin?error=github_unavailable"},
		{"token rejected", func(e *env) { e.gh.userStatus = 401 }, "/signin?error=github_refused"},
		{"exchange refused", func(e *env) { e.gh.tokenBody = `{"error":"bad_verification_code"}` }, "/signin?error=github_refused"},
		{"exchange gives no token", func(e *env) { e.gh.tokenBody = `{}` }, "/signin?error=github_unavailable"},
		{"revoke keeps failing", func(e *env) { e.gh.revokeCode = 500 }, "/signin?error=github_unavailable"},
		{"operator lookup fails", func(e *env) { e.store.failOp = errors.New("db down") }, "/signin?error=server_error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			tt.setup(e)
			c, state := e.start()
			rec := e.callback(c, state, "")
			wantRedirect(t, rec, tt.want)
			if len(e.store.sessions) != 0 {
				t.Fatalf("a refusal created a session")
			}
			// The GitHub token never outlives the attempt, whatever the outcome (an exchange that gave none has nothing to revoke).
			if tt.name != "exchange refused" && tt.name != "exchange gives no token" && len(e.gh.revoked) == 0 {
				t.Fatalf("token not revoked")
			}
		})
	}
}

func TestCallbackStateChecks(t *testing.T) {
	t.Run("no cookie", func(t *testing.T) {
		e := newEnv(t)
		_, state := e.start()
		rec := e.callback(nil, state, "")
		wantRedirect(t, rec, "/signin?error=state_check")
		if e.gh.exchanges != 0 {
			t.Fatal("GitHub was called")
		}
	})
	t.Run("wrong state", func(t *testing.T) {
		e := newEnv(t)
		c, _ := e.start()
		wantRedirect(t, e.callback(c, "tampered", ""), "/signin?error=state_check")
		if e.gh.exchanges != 0 {
			t.Fatal("GitHub was called")
		}
	})
	t.Run("empty state", func(t *testing.T) {
		e := newEnv(t)
		c, _ := e.start()
		wantRedirect(t, e.callback(c, "", ""), "/signin?error=state_check")
	})
	t.Run("unknown cookie", func(t *testing.T) {
		e := newEnv(t)
		_, state := e.start()
		wantRedirect(t, e.callback(&http.Cookie{Name: oauthCookie, Value: "forged"}, state, ""), "/signin?error=state_check")
	})
	t.Run("another flow's state", func(t *testing.T) {
		e := newEnv(t)
		c1, _ := e.start()
		_, s2 := e.start()
		wantRedirect(t, e.callback(c1, s2, ""), "/signin?error=state_check")
	})
	t.Run("a bad attempt burns the state", func(t *testing.T) {
		e := newEnv(t)
		c, state := e.start()
		wantRedirect(t, e.callback(c, "tampered", ""), "/signin?error=state_check")
		wantRedirect(t, e.callback(c, state, ""), "/signin?error=state_check")
	})
	t.Run("reused", func(t *testing.T) {
		e := newEnv(t)
		c, state := e.start()
		wantRedirect(t, e.callback(c, state, ""), "/")
		wantRedirect(t, e.callback(c, state, ""), "/signin?error=state_check")
		if len(e.store.sessions) != 1 || e.gh.exchanges != 1 {
			t.Fatalf("sessions %d exchanges %d", len(e.store.sessions), e.gh.exchanges)
		}
	})
	t.Run("expired", func(t *testing.T) {
		e := newEnv(t)
		c, state := e.start()
		e.advance(stateTTL + time.Second)
		wantRedirect(t, e.callback(c, state, ""), "/signin?error=state_check")
		if e.gh.exchanges != 0 {
			t.Fatal("GitHub was called")
		}
	})
	t.Run("github error parameter", func(t *testing.T) {
		e := newEnv(t)
		c, state := e.start()
		wantRedirect(t, e.callback(c, state, "&error=access_denied"), "/signin?error=github_refused")
		if e.gh.exchanges != 0 {
			t.Fatal("GitHub was called")
		}
	})
	t.Run("no code", func(t *testing.T) {
		e := newEnv(t)
		c, state := e.start()
		rec := e.do(http.MethodGet, callbackPath+"?state="+url.QueryEscape(state), c)
		wantRedirect(t, rec, "/signin?error=github_refused")
	})
}

func TestPendingTableIsBounded(t *testing.T) {
	e := newEnv(t)
	for range maxPending {
		e.start()
	}
	rec := e.do(http.MethodPost, startPath)
	if rec.Header().Get("Location") != "/signin?error=server_error" {
		t.Fatalf("a full table accepted a start: %q", rec.Header().Get("Location"))
	}
	e.advance(stateTTL + time.Second)
	e.start() // expired flows make room
}

func signedIn(t *testing.T, e *env) *http.Cookie {
	t.Helper()
	c, state := e.start()
	s := cookieNamed(e.callback(c, state, ""), sessionCookie)
	if s == nil {
		t.Fatal("no session")
	}
	return s
}

func TestLandingRefusals(t *testing.T) {
	t.Run("no cookie", func(t *testing.T) {
		wantRedirect(t, newEnv(t).do(http.MethodGet, "/"), "/signin")
	})
	t.Run("unknown token", func(t *testing.T) {
		wantRedirect(t, newEnv(t).do(http.MethodGet, "/", &http.Cookie{Name: sessionCookie, Value: "nope"}), "/signin")
	})
	t.Run("expired after an hour, no sliding", func(t *testing.T) {
		e := newEnv(t)
		s := signedIn(t, e)
		e.advance(59 * time.Minute)
		if rec := e.do(http.MethodGet, "/", s); rec.Code != 200 {
			t.Fatalf("still valid at 59 minutes: %d", rec.Code)
		}
		e.advance(2 * time.Minute) // the request above did not extend it
		rec := e.do(http.MethodGet, "/", s)
		wantRedirect(t, rec, "/signin")
		if strings.Contains(rec.Body.String(), "ada") {
			t.Fatal("identity shown")
		}
	})
	t.Run("revoked session", func(t *testing.T) {
		e := newEnv(t)
		s := signedIn(t, e)
		for _, ss := range e.store.sessions {
			ss.revoked = true
		}
		wantRedirect(t, e.do(http.MethodGet, "/", s), "/signin")
	})
	t.Run("revoked operator", func(t *testing.T) {
		e := newEnv(t)
		s := signedIn(t, e)
		e.store.operators[1001].revoked = true
		wantRedirect(t, e.do(http.MethodGet, "/", s), "/signin")
	})
}

func TestSignout(t *testing.T) {
	e := newEnv(t)
	s := signedIn(t, e)
	rec := e.post("/signout", url.Values{csrfField: {pageToken(t, e, s)}}, nil, s)
	wantRedirect(t, rec, "/signin")
	if c := cookieNamed(rec, sessionCookie); c == nil || c.MaxAge >= 0 {
		t.Fatalf("cookie not cleared: %+v", c)
	}
	wantRedirect(t, e.do(http.MethodGet, "/", s), "/signin")
	// Without a session it is harmless; GET is not a sign-out.
	wantRedirect(t, e.do(http.MethodPost, "/signout"), "/signin")
	if rec := e.do(http.MethodGet, "/signout"); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /signout: %d", rec.Code)
	}
}

func TestRoutesNeedHost(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"/signin", "/", callbackPath} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		req.Host = "localhost:8090"
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if rec.Code != http.StatusMisdirectedRequest {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
}

func TestNoSigninRoutesWithoutDeps(t *testing.T) {
	h := New(fakeReady{}, Deps{})
	for _, p := range []string{"/signin", "/"} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		req.Host = Host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
}
