package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kibanbuild/dojo/internal/server/repo"
)

var (
	goodToken    = strings.Repeat("a", tokenLen)
	goodCLIToken = cliTokenPrefix + strings.Repeat("b", tokenLen)
)

type fakeSessions struct {
	row       *repo.GetActiveSessionRow // nil = no active session
	learner   repo.Learner
	hashes    [][]byte
	touched   int
	revoked   []pgtype.UUID
	revokeErr error

	cli        *repo.GetActiveCLITokenRow // nil = no active CLI token
	cliErr     error                      // a database failure on the bearer lookup
	cliHashes  [][]byte
	cliTouched int
	cliRevoked []pgtype.UUID

	devs   []*fakeDevice
	devErr error

	updates   []repo.UpdateLearnerProfileParams
	updateErr error

	telemetry    []repo.InsertTelemetryEventsParams
	telemetryErr error
	learnerErr   error

	deleted    []pgtype.UUID
	deleteErr  error
	deleteGone bool // delete_learner answers false: the learner is already gone
}

func (f *fakeSessions) GetActiveSession(_ context.Context, h []byte) (repo.GetActiveSessionRow, error) {
	f.hashes = append(f.hashes, h)
	if f.row == nil || string(h) != string(HashToken(goodToken)) {
		return repo.GetActiveSessionRow{}, errors.New("no rows")
	}
	return *f.row, nil
}

func (f *fakeSessions) TouchSession(context.Context, pgtype.UUID) error { f.touched++; return nil }

// RevokeSession ends the session the way the database does: it is no longer active.
func (f *fakeSessions) RevokeSession(_ context.Context, id pgtype.UUID) error {
	if f.revokeErr != nil {
		return f.revokeErr
	}
	f.revoked = append(f.revoked, id)
	f.row = nil
	return nil
}

func (f *fakeSessions) GetActiveCLIToken(_ context.Context, h []byte) (repo.GetActiveCLITokenRow, error) {
	f.cliHashes = append(f.cliHashes, h)
	if f.cliErr != nil {
		return repo.GetActiveCLITokenRow{}, f.cliErr
	}
	if f.cli == nil || string(h) != string(HashToken(goodCLIToken)) {
		return repo.GetActiveCLITokenRow{}, pgx.ErrNoRows
	}
	return *f.cli, nil
}

func (f *fakeSessions) TouchCLIToken(context.Context, pgtype.UUID) error { f.cliTouched++; return nil }

// RevokeCLIToken ends the token the way the database does: it is no longer active.
func (f *fakeSessions) RevokeCLIToken(_ context.Context, id pgtype.UUID) error {
	if f.revokeErr != nil {
		return f.revokeErr
	}
	f.cliRevoked = append(f.cliRevoked, id)
	f.cli = nil
	return nil
}

func (f *fakeSessions) GetLearnerByID(context.Context, pgtype.UUID) (repo.Learner, error) {
	if f.learnerErr != nil {
		return repo.Learner{}, f.learnerErr
	}
	return f.learner, nil
}

func activeRow(lastUsed time.Time, suspended bool) *repo.GetActiveSessionRow {
	r := &repo.GetActiveSessionRow{
		LearnerID:  pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		LastUsedAt: pgtype.Timestamptz{Time: lastUsed, Valid: true},
	}
	if suspended {
		r.SuspendedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	}
	return r
}

func activeCLI(lastUsed time.Time, suspended bool) *repo.GetActiveCLITokenRow {
	r := &repo.GetActiveCLITokenRow{
		ID:         pgtype.UUID{Bytes: [16]byte{7}, Valid: true},
		LearnerID:  pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		LastUsedAt: pgtype.Timestamptz{Time: lastUsed, Valid: true},
	}
	if suspended {
		r.SuspendedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	}
	return r
}

func TestMeAuthentication(t *testing.T) {
	tests := []struct {
		name       string
		row        *repo.GetActiveSessionRow
		cookie     string
		authHeader string
		wantStatus int
		wantBody   string
		wantDB     bool
	}{
		{"no cookie", activeRow(time.Now(), false), "", "", 401, `{"error":"unauthenticated"}`, false},
		{"cookie of wrong length never reaches the db", activeRow(time.Now(), false), "x' OR 1=1 --", "", 401, `{"error":"unauthenticated"}`, false},
		{"unknown, revoked or expired token", nil, goodToken, "", 401, `{"error":"unauthenticated"}`, true},
		{"valid", activeRow(time.Now(), false), goodToken, "", 200, `"username":"ada"`, true},
		{"suspended", activeRow(time.Now(), true), goodToken, "", 403, `{"error":"account_suspended"}`, true},
		{"bearer and valid cookie is not cookie-authenticated", activeRow(time.Now(), false), goodToken, "Bearer abc", 401, `{"error":"unauthenticated"}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeSessions{row: tt.row, learner: repo.Learner{Username: "ada", Language: "en"}}
			req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
			if tt.cookie != "" {
				req.AddCookie(&http.Cookie{Name: SessionCookie, Value: tt.cookie})
			}
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			rec := httptest.NewRecorder()
			New(fakeReady{}, Auth{Store: f}, NewRateLimits(1), nil, nil).ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus || !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Fatalf("got %d %q", rec.Code, rec.Body.String())
			}
			if (len(f.hashes) > 0) != tt.wantDB {
				t.Fatalf("db lookups = %d, want lookup: %v", len(f.hashes), tt.wantDB)
			}
			for _, h := range f.hashes {
				if len(h) != 32 {
					t.Fatalf("lookup was not a sha256: %x", h)
				}
			}
		})
	}
}

func TestSessionTouchIsCoarse(t *testing.T) {
	for _, tt := range []struct {
		name     string
		lastUsed time.Duration
		want     int
	}{{"just used", time.Second, 0}, {"stale", 2 * time.Minute, 1}} {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeSessions{row: activeRow(time.Now().Add(-tt.lastUsed), false)}
			req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
			req.AddCookie(&http.Cookie{Name: SessionCookie, Value: goodToken})
			New(fakeReady{}, Auth{Store: f}, NewRateLimits(1), nil, nil).ServeHTTP(httptest.NewRecorder(), req)
			if f.touched != tt.want {
				t.Fatalf("touched = %d, want %d", f.touched, tt.want)
			}
		})
	}
}

func TestCSRF(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	tests := []struct {
		name        string
		method      string
		contentType string
		header      string
		cookie      bool
		bearer      bool
		want        int
	}{
		{"GET needs nothing", "GET", "", "", true, false, 204},
		{"HEAD needs nothing", "HEAD", "", "", true, false, 204},
		{"OPTIONS needs nothing", "OPTIONS", "", "", true, false, 204},
		{"POST with both", "POST", "application/json", "1", true, false, 204},
		{"POST json with charset", "POST", "application/json; charset=utf-8", "1", true, false, 204},
		{"POST without header", "POST", "application/json", "", true, false, 403},
		{"POST without content type", "POST", "", "1", true, false, 403},
		{"POST form content type", "POST", "application/x-www-form-urlencoded", "1", true, false, 403},
		{"POST text/plain", "POST", "text/plain", "1", true, false, 403},
		{"POST json-patch", "POST", "application/json-patch+json", "1", true, false, 403},
		{"POST text/plain with json param", "POST", "text/plain;application/json", "1", true, false, 403},
		{"PUT without header", "PUT", "application/json", "", true, false, 403},
		{"PATCH without header", "PATCH", "application/json", "", true, false, 403},
		{"DELETE without header", "DELETE", "application/json", "", true, false, 403},
		{"bearer is exempt", "POST", "", "", false, true, 204},
		{"bearer with cookie is exempt (cookie ignored)", "POST", "", "", true, true, 204},
		{"anonymous is exempt", "POST", "", "", false, false, 204},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeSessions{row: activeRow(time.Now(), false), cli: activeCLI(time.Now(), false)}
			req := httptest.NewRequest(tt.method, "/x", nil)
			if tt.contentType != "" {
				req.Header.Set("Content-Type", tt.contentType)
			}
			if tt.header != "" {
				req.Header.Set(CSRFHeader, tt.header)
			}
			if tt.cookie {
				req.AddCookie(&http.Cookie{Name: SessionCookie, Value: goodToken})
			}
			if tt.bearer {
				req.Header.Set("Authorization", "Bearer "+goodCLIToken)
			}
			rec := httptest.NewRecorder()
			Auth{Store: f}.Session(CSRF(ok)).ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("got %d, want %d", rec.Code, tt.want)
			}
			if tt.want == 403 && rec.Body.String() != "{\"error\":\"csrf_rejected\"}\n" {
				t.Fatalf("body %q", rec.Body.String())
			}
		})
	}
}

func TestParseOrigin(t *testing.T) {
	tests := []struct {
		env, origin string
		secure      bool
		wantErr     bool
	}{
		{"local", "http://localhost:8080", false, false},
		{"local", "https://localhost", true, false},
		{"pre", "https://pre.example.dev", true, false},
		{"pro", "https://dojo.example.dev", true, false},
		{"pre", "http://pre.example.dev", false, true},
		{"pro", "http://localhost:8080", false, true},
		{"pro", "", false, true},
		{"local", "localhost:8080", false, true},
		{"local", "", false, true},
		{"", "https://x.dev", false, true},
		{"prod", "https://x.dev", false, true},
	}
	for _, tt := range tests {
		secure, err := ParseOrigin(tt.env, tt.origin)
		if (err != nil) != tt.wantErr || secure != tt.secure {
			t.Errorf("ParseOrigin(%q, %q) = %v, %v", tt.env, tt.origin, secure, err)
		}
	}
}

func TestCookies(t *testing.T) {
	for _, secure := range []bool{false, true} {
		a := Auth{Secure: secure}
		c := a.SetCookie(goodToken)
		if c.Name != "dojo_session" || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" ||
			c.MaxAge != 30*24*3600 || c.Secure != secure {
			t.Fatalf("set cookie = %+v", c)
		}
		if cc := a.ClearCookie(); cc.MaxAge >= 0 || cc.Value != "" || cc.Secure != secure || !cc.HttpOnly {
			t.Fatalf("clear cookie = %+v", cc)
		}
	}
}

func logoutReq(cookie bool, headers map[string]string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/logout", nil)
	if cookie {
		req.AddCookie(&http.Cookie{Name: SessionCookie, Value: goodToken})
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req
}

var csrfHeaders = map[string]string{"Content-Type": "application/json", CSRFHeader: "1"}

func TestLogout(t *testing.T) {
	sid := pgtype.UUID{Bytes: [16]byte{9}, Valid: true}
	newF := func() *fakeSessions {
		f := &fakeSessions{row: activeRow(time.Now(), false)}
		f.row.ID = sid
		return f
	}
	t.Run("no principal is 401 and revokes nothing", func(t *testing.T) {
		f := newF()
		rec := httptest.NewRecorder()
		New(fakeReady{}, Auth{Store: f}, NewRateLimits(1), nil, nil).ServeHTTP(rec, logoutReq(false, nil))
		if rec.Code != 401 || len(f.revoked) != 0 || rec.Header().Get("Set-Cookie") != "" {
			t.Fatalf("got %d, revoked %v, cookie %q", rec.Code, f.revoked, rec.Header().Get("Set-Cookie"))
		}
	})
	t.Run("bearer is 401 even with a valid cookie", func(t *testing.T) {
		f := newF()
		rec := httptest.NewRecorder()
		h := New(fakeReady{}, Auth{Store: f}, NewRateLimits(1), nil, nil)
		h.ServeHTTP(rec, logoutReq(true, map[string]string{"Authorization": "Bearer abc"}))
		if rec.Code != 401 || len(f.revoked) != 0 {
			t.Fatalf("got %d, revoked %v", rec.Code, f.revoked)
		}
	})
	t.Run("without the CSRF header is 403 and revokes nothing", func(t *testing.T) {
		for _, hdr := range []map[string]string{nil, {"Content-Type": "application/json"}, {CSRFHeader: "1"}} {
			f := newF()
			rec := httptest.NewRecorder()
			New(fakeReady{}, Auth{Store: f}, NewRateLimits(1), nil, nil).ServeHTTP(rec, logoutReq(true, hdr))
			if rec.Code != 403 || len(f.revoked) != 0 {
				t.Fatalf("%v: got %d, revoked %v", hdr, rec.Code, f.revoked)
			}
		}
	})
	for _, secure := range []bool{false, true} {
		t.Run(fmt.Sprintf("success secure=%v: 204, cookie cleared, session revoked, then 401", secure), func(t *testing.T) {
			f := newF()
			h := New(fakeReady{}, Auth{Store: f, Secure: secure}, NewRateLimits(1), nil, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, logoutReq(true, csrfHeaders))
			if rec.Code != 204 || rec.Body.Len() != 0 {
				t.Fatalf("got %d %q", rec.Code, rec.Body.String())
			}
			if len(f.revoked) != 1 || f.revoked[0] != sid {
				t.Fatalf("revoked %v, want the presented session", f.revoked)
			}
			cs := rec.Result().Cookies()
			if len(cs) != 1 {
				t.Fatalf("cookies %v", cs)
			}
			c, want := cs[0], (Auth{Secure: secure}).SetCookie("")
			if c.Name != SessionCookie || c.Value != "" || c.MaxAge != -1 || c.Path != want.Path || !c.HttpOnly ||
				c.SameSite != want.SameSite || c.Secure != secure {
				t.Fatalf("cookie %+v", c)
			}
			if !strings.Contains(rec.Header().Get("Set-Cookie"), "Max-Age=0") {
				t.Fatalf("Set-Cookie %q", rec.Header().Get("Set-Cookie"))
			}
			again := httptest.NewRecorder()
			h.ServeHTTP(again, logoutReq(true, csrfHeaders))
			if again.Code != 401 || len(f.revoked) != 1 {
				t.Fatalf("second call: %d, revoked %v", again.Code, f.revoked)
			}
		})
	}
	t.Run("a failed revoke is 500 and keeps the cookie", func(t *testing.T) {
		f := newF()
		f.revokeErr = errors.New("db down")
		rec := httptest.NewRecorder()
		New(fakeReady{}, Auth{Store: f}, NewRateLimits(1), nil, nil).ServeHTTP(rec, logoutReq(true, csrfHeaders))
		if rec.Code != 500 || rec.Header().Get("Set-Cookie") != "" {
			t.Fatalf("got %d, cookie %q", rec.Code, rec.Header().Get("Set-Cookie"))
		}
	})
	t.Run("a suspended learner can still sign out", func(t *testing.T) {
		f := newF()
		f.row = activeRow(time.Now(), true)
		rec := httptest.NewRecorder()
		New(fakeReady{}, Auth{Store: f}, NewRateLimits(1), nil, nil).ServeHTTP(rec, logoutReq(true, csrfHeaders))
		if rec.Code != 204 || len(f.revoked) != 1 {
			t.Fatalf("got %d, revoked %v", rec.Code, f.revoked)
		}
	})
}
