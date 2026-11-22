package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kibanbuild/dojo/internal/github"
	"github.com/kibanbuild/dojo/internal/server/repo"
)

const ghToken = "gho_secretdevicetoken0123456789abcdef"

type fakeCLIGitHub struct {
	info      github.TokenInfo
	checkErr  error
	emails    string
	emailsErr error
	revokeErr error
	// revokeFails is the number of transient revoke failures before it works.
	revokeFails int

	checked, revoked, emailCalls int
	order                        []string
}

func (f *fakeCLIGitHub) CheckToken(_ context.Context, tok string) (github.TokenInfo, error) {
	f.checked++
	f.order = append(f.order, "check")
	if tok != ghToken {
		return github.TokenInfo{}, github.ErrRejected
	}
	return f.info, f.checkErr
}

func (f *fakeCLIGitHub) Emails(context.Context, string) (string, error) {
	f.emailCalls++
	f.order = append(f.order, "emails")
	return f.emails, f.emailsErr
}

func (f *fakeCLIGitHub) Revoke(context.Context, string) error {
	f.revoked++
	f.order = append(f.order, "revoke")
	if f.revokeFails > 0 {
		f.revokeFails--
		return github.ErrUnavailable
	}
	return f.revokeErr
}

type fakeCLIStore struct {
	known     *repo.Learner
	lookupErr error
	upserts   []repo.UpsertLearnerParams
	inserted  []repo.InsertCLITokenParams
	insertErr error
}

func (f *fakeCLIStore) GetLearnerByGithubID(context.Context, int64) (repo.Learner, error) {
	if f.lookupErr != nil {
		return repo.Learner{}, f.lookupErr
	}
	if f.known == nil {
		return repo.Learner{}, pgx.ErrNoRows
	}
	return *f.known, nil
}

func (f *fakeCLIStore) UpsertLearner(_ context.Context, a repo.UpsertLearnerParams) (repo.Learner, error) {
	f.upserts = append(f.upserts, a)
	return *f.known, nil
}

func (f *fakeCLIStore) InsertCLIToken(_ context.Context, a repo.InsertCLITokenParams) (repo.InsertCLITokenRow, error) {
	f.inserted = append(f.inserted, a)
	return repo.InsertCLITokenRow{}, f.insertErr
}

func existingLearner() *repo.Learner {
	return &repo.Learner{ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, OrgID: pgtype.UUID{Bytes: [16]byte{9}, Valid: true},
		Username: "ada", Language: "en", GithubID: 1001, UsageStats: true}
}

type cliRig struct {
	gh *fakeCLIGitHub
	st *fakeCLIStore
	h  http.Handler
}

func newCLIRig() *cliRig {
	r := &cliRig{
		gh: &fakeCLIGitHub{info: github.TokenInfo{UserID: 1001, Login: "ada", Scopes: []string{"read:user", "user:email"}}, emails: "ada@example.test"},
		st: &fakeCLIStore{known: existingLearner()},
	}
	cli := &CLILogin{GitHub: r.gh, Store: r.st, Backoff: []time.Duration{time.Millisecond, time.Millisecond}}
	r.h = New(fakeReady{}, Auth{Store: &fakeSessions{}}, NewRateLimits(1), nil, cli)
	return r
}

const goodBody = `{"access_token":"` + ghToken + `","device_name":"Ada's MacBook","os":"darwin/arm64","cli_version":"v0.3.1"}`

func (r *cliRig) post(body string, mut func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, cliPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if mut != nil {
		mut(req)
	}
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	return rec
}

func TestCLILoginSuccess(t *testing.T) {
	r := newCLIRig()
	rec := r.post(goodBody, nil)
	var out struct{ Token string }
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &out) != nil || !validCLIToken(out.Token) {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control %q", rec.Header().Get("Cache-Control"))
	}
	if len(r.st.inserted) != 1 {
		t.Fatalf("inserted %d tokens", len(r.st.inserted))
	}
	in := r.st.inserted[0]
	if string(in.TokenHash) != string(HashToken(out.Token)) || len(in.TokenHash) != 32 || strings.Contains(fmt.Sprintf("%v", in), out.Token) {
		t.Fatalf("stored %+v: only the sha256 of the token may be stored", in)
	}
	if in.DeviceName != "Ada's MacBook" || in.Os != "darwin/arm64" || in.CliVersion != "v0.3.1" || in.LearnerID != r.st.known.ID || in.OrgID != r.st.known.OrgID {
		t.Fatalf("device fields %+v", in)
	}
	if strings.Contains(fmt.Sprintf("%v", in), ghToken) {
		t.Fatal("the GitHub token reached the store")
	}
	// check, then email, then revoke, all before anything is issued.
	if got := strings.Join(r.gh.order, ","); got != "check,emails,revoke" {
		t.Fatalf("GitHub calls: %s", got)
	}
	// Like the web flow: login and email are refreshed, the learner's own fields are kept.
	if len(r.st.upserts) != 1 {
		t.Fatalf("upserts %d", len(r.st.upserts))
	}
	u := r.st.upserts[0]
	if u.GithubLogin != "ada" || u.Email.String != "ada@example.test" || !u.Email.Valid || u.Username != "ada" {
		t.Fatalf("upsert %+v", u)
	}
	if strings.Contains(rec.Body.String(), ghToken) {
		t.Fatal("response holds the GitHub token")
	}
}

func TestCLILoginTokensAreUnique(t *testing.T) {
	r := newCLIRig()
	var seen = map[string]bool{}
	for range 5 {
		var out struct{ Token string }
		_ = json.Unmarshal(r.post(goodBody, nil).Body.Bytes(), &out)
		if seen[out.Token] || !validCLIToken(out.Token) {
			t.Fatalf("token %q repeated or malformed", out.Token)
		}
		seen[out.Token] = true
	}
}

func TestCLILoginErrors(t *testing.T) {
	suspended := existingLearner()
	suspended.SuspendedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	tests := []struct {
		name        string
		body        string
		mut         func(*http.Request)
		setup       func(*cliRig)
		want        int
		wantCode    string
		wantRevoked int
		wantChecked int
	}{
		{name: "bad json", body: `{`, want: 400, wantCode: "bad_request"},
		{name: "empty body", body: ``, want: 400, wantCode: "bad_request"},
		{name: "wrong content type", body: goodBody, mut: func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, want: 400, wantCode: "bad_request"},
		{name: "oversized body", body: `{"access_token":"` + strings.Repeat("a", 5000) + `"}`, want: 400, wantCode: "bad_request"},
		{name: "missing token", body: `{"device_name":"x","os":"linux","cli_version":"1"}`, want: 400, wantCode: "bad_request"},
		{name: "token with a bad charset", body: strings.Replace(goodBody, ghToken, "gho_ bad\\u0000", 1), want: 400, wantCode: "bad_request"},
		{name: "empty device name", body: strings.Replace(goodBody, "Ada's MacBook", "", 1), want: 400, wantCode: "bad_request"},
		{name: "device name too long", body: strings.Replace(goodBody, "Ada's MacBook", strings.Repeat("x", 65), 1), want: 400, wantCode: "bad_request"},
		{name: "device name with a control character", body: strings.Replace(goodBody, "Ada's MacBook", `a\u0007b`, 1), want: 400, wantCode: "bad_request"},
		{name: "device name with a bidi override", body: strings.Replace(goodBody, "Ada's MacBook", `a\u202eb`, 1), want: 400, wantCode: "bad_request"},
		{name: "device name with a zero-width space", body: strings.Replace(goodBody, "Ada's MacBook", `a\u200bb`, 1), want: 400, wantCode: "bad_request"},
		{name: "device name with a newline", body: strings.Replace(goodBody, "Ada's MacBook", `a\nb`, 1), want: 400, wantCode: "bad_request"},
		{name: "os with a bad charset", body: strings.Replace(goodBody, "darwin/arm64", "darwin<script>", 1), want: 400, wantCode: "bad_request"},
		{name: "os too long", body: strings.Replace(goodBody, "darwin/arm64", strings.Repeat("a", 33), 1), want: 400, wantCode: "bad_request"},
		{name: "version with a space", body: strings.Replace(goodBody, "v0.3.1", "v0 3", 1), want: 400, wantCode: "bad_request"},
		{name: "version too long", body: strings.Replace(goodBody, "v0.3.1", strings.Repeat("1", 33), 1), want: 400, wantCode: "bad_request"},
		{name: "token of another app or unknown (check 404)", body: strings.Replace(goodBody, ghToken, "gho_other", 1), want: 401, wantCode: "github_rejected", wantChecked: 1},
		{name: "check says another client id", body: goodBody, setup: func(r *cliRig) { r.gh.checkErr = fmt.Errorf("check token: %w", github.ErrRejected) },
			want: 401, wantCode: "github_rejected", wantChecked: 1},
		{name: "github 5xx on check", body: goodBody, setup: func(r *cliRig) { r.gh.checkErr = github.ErrUnavailable }, want: 502, wantCode: "github_unavailable", wantChecked: 1},
		{name: "revoke keeps failing: no token issued", body: goodBody, setup: func(r *cliRig) { r.gh.revokeErr = github.ErrUnavailable },
			want: 502, wantCode: "github_unavailable", wantRevoked: 3, wantChecked: 1},
		{name: "emails fails: still revokes, no token", body: goodBody, setup: func(r *cliRig) { r.gh.emailsErr = github.ErrUnavailable },
			want: 502, wantCode: "github_unavailable", wantRevoked: 1, wantChecked: 1},
		{name: "unknown account: sign up on the web first", body: goodBody, setup: func(r *cliRig) { r.st.known = nil },
			want: 403, wantCode: "signup_required", wantRevoked: 1, wantChecked: 1},
		{name: "suspended learner", body: goodBody, setup: func(r *cliRig) { r.st.known = suspended },
			want: 403, wantCode: "account_suspended", wantRevoked: 1, wantChecked: 1},
		{name: "database error on lookup", body: goodBody, setup: func(r *cliRig) { r.st.lookupErr = errors.New("db down") },
			want: 500, wantCode: "server_error", wantRevoked: 1, wantChecked: 1},
		{name: "database error on insert", body: goodBody, setup: func(r *cliRig) { r.st.insertErr = errors.New("db down") },
			want: 500, wantCode: "server_error", wantRevoked: 1, wantChecked: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newCLIRig()
			if tt.setup != nil {
				tt.setup(r)
			}
			rec := r.post(tt.body, tt.mut)
			if rec.Code != tt.want || rec.Body.String() != fmt.Sprintf("{\"error\":%q}\n", tt.wantCode) {
				t.Fatalf("got %d %q", rec.Code, rec.Body.String())
			}
			if r.gh.revoked != tt.wantRevoked || r.gh.checked != tt.wantChecked {
				t.Fatalf("github calls: checked %d revoked %d, want %d and %d", r.gh.checked, r.gh.revoked, tt.wantChecked, tt.wantRevoked)
			}
			if tt.want != http.StatusInternalServerError && len(r.st.inserted) != 0 {
				t.Fatalf("a token was stored on a refused login: %+v", r.st.inserted)
			}
			if strings.Contains(rec.Body.String(), ghToken) {
				t.Fatal("response echoes the GitHub token")
			}
		})
	}
}

func TestCLILoginNoEmailGrantStillLogsIn(t *testing.T) {
	r := newCLIRig()
	r.gh.info.Scopes = []string{"read:user"}
	if rec := r.post(goodBody, nil); rec.Code != http.StatusCreated {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
	if r.gh.emailCalls != 0 || r.st.upserts[0].Email.Valid {
		t.Fatalf("emails asked %d times, upsert email %+v", r.gh.emailCalls, r.st.upserts[0].Email)
	}
}

func TestCLILoginTransientRevokeFailureRetries(t *testing.T) {
	r := newCLIRig()
	r.gh.revokeFails = 2
	if rec := r.post(goodBody, nil); rec.Code != http.StatusCreated || r.gh.revoked != 3 {
		t.Fatalf("got %d, revoked %d", rec.Code, r.gh.revoked)
	}
}

func TestCLILoginSuspendedLearnerIsNotUpdated(t *testing.T) {
	r := newCLIRig()
	r.st.known.SuspendedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	r.post(goodBody, nil)
	if len(r.st.upserts) != 0 {
		t.Fatal("a suspended learner was updated")
	}
}

// The route authenticates by its body: a stale Authorization header or cookie from an earlier login
// must not turn a login into a 401 or a CSRF 403.
func TestCLILoginIgnoresOtherCredentials(t *testing.T) {
	for name, mut := range map[string]func(*http.Request){
		"stale bearer": func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+goodCLIToken) },
		"garbage":      func(r *http.Request) { r.Header.Set("Authorization", "Basic xyz") },
		"cookie":       func(r *http.Request) { r.AddCookie(&http.Cookie{Name: SessionCookie, Value: goodToken}) },
	} {
		t.Run(name, func(t *testing.T) {
			r := newCLIRig()
			if rec := r.post(goodBody, mut); rec.Code != http.StatusCreated {
				t.Fatalf("got %d %q", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestCLILoginIsRateLimitedAndRejectsOtherMethods(t *testing.T) {
	r := newCLIRig()
	var last int
	for range authBurst + 1 {
		last = r.post(`{`, func(r *http.Request) { r.Header.Set("X-Forwarded-For", "203.0.113.9") }).Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("after the burst: %d, want 429", last)
	}
	req := httptest.NewRequest(http.MethodGet, cliPath, nil)
	rec := httptest.NewRecorder()
	newCLIRig().h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d", rec.Code)
	}
}

// No log line may hold the GitHub token, on any path.
func TestCLILoginNeverLogsTheGitHubToken(t *testing.T) {
	var buf strings.Builder
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })
	for _, setup := range []func(*cliRig){
		func(*cliRig) {},
		func(r *cliRig) { r.gh.revokeErr = errors.New("revoke: status 500") },
		func(r *cliRig) { r.st.known = nil },
		func(r *cliRig) { r.gh.checkErr = github.ErrUnavailable },
	} {
		r := newCLIRig()
		setup(r)
		r.post(goodBody, nil)
	}
	if strings.Contains(buf.String(), ghToken) {
		t.Fatalf("log holds the GitHub token: %s", buf.String())
	}
}

func bearerReq(method, path, header string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	if header != "" {
		req.Header.Set("Authorization", header)
	}
	return req
}

func TestBearerAuthentication(t *testing.T) {
	ok := "Bearer " + goodCLIToken
	tests := []struct {
		name       string
		header     string
		values     []string // overrides header: several Authorization headers
		cli        *repo.GetActiveCLITokenRow
		cliErr     error
		cookie     bool
		wantStatus int
		wantBody   string
		wantDB     bool
	}{
		{name: "valid", header: ok, cli: activeCLI(time.Now(), false), wantStatus: 200, wantBody: `"username":"ada"`, wantDB: true},
		{name: "scheme is case-insensitive", header: "bearer " + goodCLIToken, cli: activeCLI(time.Now(), false), wantStatus: 200, wantBody: `"username":"ada"`, wantDB: true},
		{name: "revoked or unknown", header: ok, cli: nil, wantStatus: 401, wantBody: `{"error":"unauthenticated"}`, wantDB: true},
		{name: "suspended", header: ok, cli: activeCLI(time.Now(), true), wantStatus: 403, wantBody: `{"error":"account_suspended"}`, wantDB: true},
		{name: "database failure is not anonymous", header: ok, cli: activeCLI(time.Now(), false), cliErr: errors.New("db down"), wantStatus: 500, wantBody: `{"error":"server_error"}`, wantDB: true},
		{name: "wrong scheme", header: "Basic " + goodCLIToken, cli: activeCLI(time.Now(), false), wantStatus: 401, wantBody: `{"error":"unauthenticated"}`},
		{name: "no token", header: "Bearer ", cli: activeCLI(time.Now(), false), wantStatus: 401, wantBody: `{"error":"unauthenticated"}`},
		{name: "bare scheme", header: "Bearer", cli: activeCLI(time.Now(), false), wantStatus: 401, wantBody: `{"error":"unauthenticated"}`},
		{name: "no prefix", header: "Bearer " + strings.Repeat("b", cliTokenLen), cli: activeCLI(time.Now(), false), wantStatus: 401, wantBody: `{"error":"unauthenticated"}`},
		{name: "session cookie value as bearer", header: "Bearer " + goodToken, cli: activeCLI(time.Now(), false), wantStatus: 401, wantBody: `{"error":"unauthenticated"}`},
		{name: "too long", header: ok + "b", cli: activeCLI(time.Now(), false), wantStatus: 401, wantBody: `{"error":"unauthenticated"}`},
		{name: "bad charset", header: "Bearer " + cliTokenPrefix + strings.Repeat("b", tokenLen-1) + "'", cli: activeCLI(time.Now(), false), wantStatus: 401, wantBody: `{"error":"unauthenticated"}`},
		{name: "two headers", values: []string{ok, ok}, cli: activeCLI(time.Now(), false), wantStatus: 401, wantBody: `{"error":"unauthenticated"}`},
		{name: "bad bearer with a valid cookie is not anonymous nor cookie-authenticated", header: "Bearer nope", cookie: true, wantStatus: 401, wantBody: `{"error":"unauthenticated"}`},
		{name: "valid bearer wins over a valid cookie", header: ok, cli: activeCLI(time.Now(), false), cookie: true, wantStatus: 200, wantBody: `"username":"ada"`, wantDB: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeSessions{cli: tt.cli, cliErr: tt.cliErr, row: activeRow(time.Now(), false), learner: repo.Learner{Username: "ada", Language: "en"}}
			req := bearerReq(http.MethodGet, "/v1/me", tt.header)
			if tt.values != nil {
				req.Header["Authorization"] = tt.values
			}
			if tt.cookie {
				req.AddCookie(&http.Cookie{Name: SessionCookie, Value: goodToken})
			}
			rec := httptest.NewRecorder()
			New(fakeReady{}, Auth{Store: f}, NewRateLimits(1), nil, nil).ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus || !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Fatalf("got %d %q", rec.Code, rec.Body.String())
			}
			if (len(f.cliHashes) > 0) != tt.wantDB {
				t.Fatalf("cli token lookups = %d, want lookup: %v", len(f.cliHashes), tt.wantDB)
			}
			if len(f.hashes) != 0 {
				t.Fatalf("a request with an Authorization header looked at the session cookie")
			}
			for _, h := range f.cliHashes {
				if len(h) != 32 {
					t.Fatalf("lookup was not a sha256: %x", h)
				}
			}
		})
	}
}

// A bad bearer is 401 on every route, including the ones that serve anonymous callers.
func TestBadBearerIsNeverAnonymous(t *testing.T) {
	for _, path := range []string{"/v1/health", "/v1/me", "/v1/auth/logout"} {
		f := &fakeSessions{}
		method := http.MethodGet
		if path == "/v1/auth/logout" {
			method = http.MethodPost
		}
		rec := httptest.NewRecorder()
		New(fakeReady{}, Auth{Store: f}, NewRateLimits(1), nil, nil).ServeHTTP(rec, bearerReq(method, path, "Bearer "+goodCLIToken))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s with an unknown token: %d", path, rec.Code)
		}
	}
}

func TestCLITokenTouchIsCoarse(t *testing.T) {
	for _, tt := range []struct {
		name     string
		lastUsed time.Duration
		want     int
	}{{"just used", time.Second, 0}, {"stale", 2 * time.Minute, 1}} {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeSessions{cli: activeCLI(time.Now().Add(-tt.lastUsed), false)}
			New(fakeReady{}, Auth{Store: f}, NewRateLimits(1), nil, nil).ServeHTTP(httptest.NewRecorder(), bearerReq(http.MethodGet, "/v1/me", "Bearer "+goodCLIToken))
			if f.cliTouched != tt.want {
				t.Fatalf("touched = %d, want %d", f.cliTouched, tt.want)
			}
		})
	}
}

// Bearer calls carry no ambient credential, so they need no CSRF header.
func TestBearerSkipsCSRFButCookieDoesNot(t *testing.T) {
	f := &fakeSessions{cli: activeCLI(time.Now(), false), row: activeRow(time.Now(), false)}
	h := New(fakeReady{}, Auth{Store: f}, NewRateLimits(1), nil, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, bearerReq(http.MethodDelete, cliPath, "Bearer "+goodCLIToken))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("bearer DELETE: %d %q", rec.Code, rec.Body.String())
	}
	req := bearerReq(http.MethodDelete, cliPath, "")
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: goodToken})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cookie DELETE without CSRF: %d", rec.Code)
	}
}

func TestCLILogout(t *testing.T) {
	t.Run("revokes the presenting token only", func(t *testing.T) {
		f := &fakeSessions{cli: activeCLI(time.Now(), false), row: activeRow(time.Now(), false)}
		h := New(fakeReady{}, Auth{Store: f}, NewRateLimits(1), nil, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, bearerReq(http.MethodDelete, cliPath, "Bearer "+goodCLIToken))
		if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 || len(f.cliRevoked) != 1 || len(f.revoked) != 0 {
			t.Fatalf("got %d %q, cli revoked %d, sessions revoked %d", rec.Code, rec.Body.String(), len(f.cliRevoked), len(f.revoked))
		}
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, bearerReq(http.MethodDelete, cliPath, "Bearer "+goodCLIToken))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("second logout: %d", rec.Code)
		}
	})
	t.Run("a suspended learner can still log out", func(t *testing.T) {
		f := &fakeSessions{cli: activeCLI(time.Now(), true)}
		rec := httptest.NewRecorder()
		New(fakeReady{}, Auth{Store: f}, NewRateLimits(1), nil, nil).ServeHTTP(rec, bearerReq(http.MethodDelete, cliPath, "Bearer "+goodCLIToken))
		if rec.Code != http.StatusNoContent || len(f.cliRevoked) != 1 {
			t.Fatalf("got %d, revoked %d", rec.Code, len(f.cliRevoked))
		}
	})
	t.Run("no credential or a cookie session is 401", func(t *testing.T) {
		f := &fakeSessions{row: activeRow(time.Now(), false)}
		h := New(fakeReady{}, Auth{Store: f}, NewRateLimits(1), nil, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, bearerReq(http.MethodDelete, cliPath, ""))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous: %d", rec.Code)
		}
		req := bearerReq(http.MethodDelete, cliPath, "")
		req.AddCookie(&http.Cookie{Name: SessionCookie, Value: goodToken})
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(CSRFHeader, "1")
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized || len(f.revoked) != 0 {
			t.Fatalf("cookie session: %d", rec.Code)
		}
	})
	t.Run("a revoke failure is 500", func(t *testing.T) {
		f := &fakeSessions{cli: activeCLI(time.Now(), false), revokeErr: errors.New("db down")}
		rec := httptest.NewRecorder()
		New(fakeReady{}, Auth{Store: f}, NewRateLimits(1), nil, nil).ServeHTTP(rec, bearerReq(http.MethodDelete, cliPath, "Bearer "+goodCLIToken))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("got %d", rec.Code)
		}
	})
	t.Run("web logout refuses a bearer principal", func(t *testing.T) {
		f := &fakeSessions{cli: activeCLI(time.Now(), false)}
		rec := httptest.NewRecorder()
		New(fakeReady{}, Auth{Store: f}, NewRateLimits(1), nil, nil).ServeHTTP(rec, bearerReq(http.MethodPost, logoutPath, "Bearer "+goodCLIToken))
		if rec.Code != http.StatusUnauthorized || len(f.cliRevoked) != 0 || len(f.revoked) != 0 {
			t.Fatalf("got %d", rec.Code)
		}
	})
}
