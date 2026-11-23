package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func (f *fakeSessions) DeleteLearner(_ context.Context, id pgtype.UUID) (bool, error) {
	if f.deleteErr != nil {
		return false, f.deleteErr
	}
	f.deleted = append(f.deleted, id)
	return !f.deleteGone, nil
}

type deleteCall struct {
	cookie  bool
	csrf    bool
	ctype   string // "" = application/json, "-" = none
	body    string
	suspend bool
}

func doDelete(t *testing.T, f *fakeSessions, c deleteCall) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, "/v1/me", strings.NewReader(c.body))
	if c.ctype != "-" {
		req.Header.Set("Content-Type", "application/json")
		if c.ctype != "" {
			req.Header.Set("Content-Type", c.ctype)
		}
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
	New(fakeReady{}, Auth{Store: f}, NewRateLimits(1), nil, nil).ServeHTTP(rec, req)
	return rec
}

func TestDeleteMeOK(t *testing.T) {
	for _, confirm := range []string{"Ada", "ada", "ADA"} {
		t.Run(confirm, func(t *testing.T) {
			f := newPatchFake()
			rec := doDelete(t, f, deleteCall{cookie: true, csrf: true, body: `{"confirm":"` + confirm + `"}`})
			if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
				t.Fatalf("got %d %q", rec.Code, rec.Body.String())
			}
			if len(f.deleted) != 1 || f.deleted[0] != (pgtype.UUID{Bytes: [16]byte{1}, Valid: true}) {
				t.Fatalf("deleted %v", f.deleted)
			}
			var cleared *http.Cookie
			for _, c := range rec.Result().Cookies() {
				if c.Name == SessionCookie {
					cleared = c
				}
			}
			if cleared == nil || cleared.MaxAge >= 0 || cleared.Value != "" {
				t.Fatalf("cookie not cleared: %+v", cleared)
			}
		})
	}
}

func TestDeleteMeSuspendedMayDelete(t *testing.T) {
	f := newPatchFake()
	rec := doDelete(t, f, deleteCall{cookie: true, csrf: true, suspend: true, body: `{"confirm":"ada"}`})
	if rec.Code != http.StatusNoContent || len(f.deleted) != 1 {
		t.Fatalf("got %d %q deleted=%v", rec.Code, rec.Body.String(), f.deleted)
	}
}

func TestDeleteMeRefusals(t *testing.T) {
	tests := []struct {
		name     string
		call     deleteCall
		wantCode int
		wantErr  string
	}{
		{"mismatch", deleteCall{cookie: true, csrf: true, body: `{"confirm":"bob"}`}, 400, "confirm_mismatch"},
		{"empty confirm", deleteCall{cookie: true, csrf: true, body: `{"confirm":""}`}, 400, "confirm_mismatch"},
		{"prefix is not a match", deleteCall{cookie: true, csrf: true, body: `{"confirm":"ad"}`}, 400, "confirm_mismatch"},
		{"empty body", deleteCall{cookie: true, csrf: true, body: ``}, 400, "bad_request"},
		{"empty object", deleteCall{cookie: true, csrf: true, body: `{}`}, 400, "bad_request"},
		{"null confirm", deleteCall{cookie: true, csrf: true, body: `{"confirm":null}`}, 400, "bad_request"},
		{"not a string", deleteCall{cookie: true, csrf: true, body: `{"confirm":1}`}, 400, "bad_request"},
		{"unknown field", deleteCall{cookie: true, csrf: true, body: `{"confirm":"ada","force":true}`}, 400, "bad_request"},
		{"trailing data", deleteCall{cookie: true, csrf: true, body: `{"confirm":"ada"}{}`}, 400, "bad_request"},
		{"oversized", deleteCall{cookie: true, csrf: true, body: `{"confirm":"` + strings.Repeat("a", 2000) + `"}`}, 400, "bad_request"},
		{"not json", deleteCall{cookie: true, csrf: true, body: `ada`}, 400, "bad_request"},
		{"bearer", deleteCall{body: `{"confirm":"ada"}`}, 403, "forbidden"},
		{"suspended bearer", deleteCall{suspend: true, body: `{"confirm":"ada"}`}, 403, "forbidden"},
		{"no CSRF header", deleteCall{cookie: true, body: `{"confirm":"ada"}`}, 403, "csrf_rejected"},
		{"wrong content type", deleteCall{cookie: true, csrf: true, ctype: "text/plain", body: `{"confirm":"ada"}`}, 403, "csrf_rejected"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newPatchFake()
			rec := doDelete(t, f, tt.call)
			if rec.Code != tt.wantCode || rec.Body.String() != "{\"error\":\""+tt.wantErr+"\"}\n" {
				t.Fatalf("got %d %q", rec.Code, rec.Body.String())
			}
			if len(f.deleted) != 0 {
				t.Fatalf("a refused request deleted: %v", f.deleted)
			}
			for _, c := range rec.Result().Cookies() {
				if c.Name == SessionCookie {
					t.Fatalf("a refused request touched the cookie: %+v", c)
				}
			}
		})
	}
}

func TestDeleteMeAnonymous(t *testing.T) {
	req := httptest.NewRequest(http.MethodDelete, "/v1/me", strings.NewReader(`{"confirm":"ada"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	New(fakeReady{}, Auth{Store: newPatchFake()}, NewRateLimits(1), nil, nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestDeleteMeStoreErrors(t *testing.T) {
	t.Run("delete fails", func(t *testing.T) {
		f := newPatchFake()
		f.deleteErr = context.DeadlineExceeded
		rec := doDelete(t, f, deleteCall{cookie: true, csrf: true, body: `{"confirm":"ada"}`})
		if rec.Code != http.StatusInternalServerError || rec.Body.String() != "{\"error\":\"server_error\"}\n" {
			t.Fatalf("got %d %q", rec.Code, rec.Body.String())
		}
		for _, c := range rec.Result().Cookies() {
			if c.Name == SessionCookie {
				t.Fatalf("a failed delete cleared the cookie")
			}
		}
	})
	t.Run("load fails", func(t *testing.T) {
		f := newPatchFake()
		f.learnerErr = context.DeadlineExceeded
		rec := doDelete(t, f, deleteCall{cookie: true, csrf: true, body: `{"confirm":"ada"}`})
		if rec.Code != http.StatusInternalServerError || len(f.deleted) != 0 {
			t.Fatalf("got %d deleted=%v", rec.Code, f.deleted)
		}
	})
	t.Run("already gone", func(t *testing.T) {
		f := newPatchFake()
		f.deleteGone = true
		rec := doDelete(t, f, deleteCall{cookie: true, csrf: true, body: `{"confirm":"ada"}`})
		if rec.Code != http.StatusUnauthorized || rec.Body.String() != "{\"error\":\"unauthenticated\"}\n" {
			t.Fatalf("got %d %q", rec.Code, rec.Body.String())
		}
	})
}

func TestDeleteMeIsRateLimited(t *testing.T) {
	var calls atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /v1/me", func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("PATCH /v1/me", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	h := Auth{Store: &fakeSessions{}}.Session(testLimits(2).wrap(mux, CSRF(mux)))
	codes := make([]int, 3)
	for i := range codes {
		req := httptest.NewRequest(http.MethodDelete, "/v1/me", nil)
		req.RemoteAddr = "1.1.1.1:1"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		codes[i] = rec.Code
	}
	if codes[0] != 204 || codes[1] != 204 || codes[2] != http.StatusTooManyRequests || calls.Load() != 2 {
		t.Fatalf("codes %v calls %d", codes, calls.Load())
	}
	// PATCH /v1/me stays unlimited here: only the erase route takes a token.
	for range 5 {
		req := httptest.NewRequest(http.MethodPatch, "/v1/me", nil)
		req.RemoteAddr = "2.2.2.2:1"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 204 {
			t.Fatalf("patch got %d", rec.Code)
		}
	}
}
