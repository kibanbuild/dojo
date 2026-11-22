package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kibanbuild/dojo/internal/server/repo"
)

func (f *fakeSessions) UpdateLearnerProfile(_ context.Context, a repo.UpdateLearnerProfileParams) (repo.Learner, error) {
	f.updates = append(f.updates, a)
	if f.updateErr != nil {
		return repo.Learner{}, f.updateErr
	}
	l := f.learner
	if a.Username.Valid {
		l.Username = a.Username.String
	}
	if a.Language.Valid {
		l.Language = a.Language.String
	}
	f.learner = l
	return l, nil
}

type patchCall struct {
	cookie  bool
	csrf    bool
	ctype   string
	body    string
	suspend bool
}

func doPatch(t *testing.T, f *fakeSessions, c patchCall) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/v1/me", strings.NewReader(c.body))
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

func newPatchFake() *fakeSessions {
	return &fakeSessions{learner: repo.Learner{Username: "ada", Language: "en", GithubLogin: "Ada"}}
}

func TestPatchMeValidation(t *testing.T) {
	tests := []struct {
		name, body, wantErr string
	}{
		{"empty body", ``, "bad_request"},
		{"empty object", `{}`, "bad_request"},
		{"only nulls", `{"username":null,"language":null}`, "bad_request"},
		{"not json", `nope`, "bad_request"},
		{"unknown field", `{"username":"abc","email":"x@y.z"}`, "bad_request"},
		{"trailing data", `{"username":"abc"}{}`, "bad_request"},
		{"wrong type", `{"username":5}`, "bad_request"},
		{"too short", `{"username":"ab"}`, "invalid_username"},
		{"too long", `{"username":"` + strings.Repeat("a", 31) + `"}`, "invalid_username"},
		{"bad characters", `{"username":"a_b"}`, "invalid_username"},
		{"space", `{"username":" abc"}`, "invalid_username"},
		{"empty", `{"username":""}`, "invalid_username"},
		{"reserved", `{"username":"admin"}`, "invalid_username"},
		{"reserved in capitals", `{"username":"ADMIN"}`, "invalid_username"},
		{"reserved behind a Kelvin sign", "{\"username\":\"\u212aiban\"}", "invalid_username"},
		{"reserved behind a dotted capital I", "{\"username\":\"ADM\u0130N\"}", "invalid_username"},
		{"fullwidth letters", "{\"username\":\"\uff41\uff44\uff41\"}", "invalid_username"},
		{"language unknown", `{"language":"fr"}`, "invalid_language"},
		{"language empty", `{"language":""}`, "invalid_language"},
		{"language capitals", `{"language":"EN"}`, "invalid_language"},
		{"one bad field refuses both", `{"username":"newname","language":"fr"}`, "invalid_language"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newPatchFake()
			rec := doPatch(t, f, patchCall{cookie: true, csrf: true, body: tt.body})
			if rec.Code != http.StatusBadRequest || rec.Body.String() != `{"error":"`+tt.wantErr+`"}`+"\n" {
				t.Fatalf("got %d %q", rec.Code, rec.Body.String())
			}
			if len(f.updates) != 0 {
				t.Fatalf("a refused request wrote: %+v", f.updates)
			}
		})
	}
}

func TestPatchMeNotJSONContentType(t *testing.T) {
	f := newPatchFake()
	rec := doPatch(t, f, patchCall{cookie: false, ctype: "text/plain", body: `{"username":"abc"}`})
	if rec.Code != http.StatusBadRequest || len(f.updates) != 0 {
		t.Fatalf("got %d %q, writes %d", rec.Code, rec.Body.String(), len(f.updates))
	}
}

func TestPatchMeSuccess(t *testing.T) {
	tests := []struct {
		name, body         string
		wantUser, wantLang string
		wantUserSet        bool
		wantLangSet        bool
	}{
		{"username", `{"username":"ada-2"}`, "ada-2", "en", true, false},
		{"uppercase is lowercased", `{"username":"Ada-Two"}`, "ada-two", "en", true, false},
		{"language", `{"language":"es"}`, "ada", "es", false, true},
		{"both", `{"username":"abc","language":"es"}`, "abc", "es", true, true},
		{"no-op values", `{"username":"ada","language":"en"}`, "ada", "en", true, true},
		{"null beside a value is absent", `{"username":null,"language":"es"}`, "ada", "es", false, true},
	}
	for _, mode := range []struct {
		name   string
		cookie bool
	}{{"cookie", true}, {"bearer", false}} {
		for _, tt := range tests {
			t.Run(mode.name+"/"+tt.name, func(t *testing.T) {
				f := newPatchFake()
				rec := doPatch(t, f, patchCall{cookie: mode.cookie, csrf: true, body: tt.body})
				if rec.Code != http.StatusOK {
					t.Fatalf("got %d %q", rec.Code, rec.Body.String())
				}
				if got := rec.Header().Get("Content-Type"); got != "application/json" {
					t.Fatalf("content type %q", got)
				}
				want := `"username":"` + tt.wantUser + `"`
				if !strings.Contains(rec.Body.String(), want) || !strings.Contains(rec.Body.String(), `"language":"`+tt.wantLang+`"`) ||
					!strings.Contains(rec.Body.String(), `"github_login":"Ada"`) {
					t.Fatalf("body %q", rec.Body.String())
				}
				if len(f.updates) != 1 || f.updates[0].Username.Valid != tt.wantUserSet || f.updates[0].Language.Valid != tt.wantLangSet ||
					f.updates[0].ID != (pgtype.UUID{Bytes: [16]byte{1}, Valid: true}) {
					t.Fatalf("update args %+v", f.updates)
				}
			})
		}
	}
}

func TestPatchMeRequiresCSRFForCookie(t *testing.T) {
	f := newPatchFake()
	rec := doPatch(t, f, patchCall{cookie: true, csrf: false, body: `{"username":"abc"}`})
	if rec.Code != http.StatusForbidden || rec.Body.String() != "{\"error\":\"csrf_rejected\"}\n" || len(f.updates) != 0 {
		t.Fatalf("got %d %q, writes %d", rec.Code, rec.Body.String(), len(f.updates))
	}
	rec = doPatch(t, f, patchCall{cookie: true, csrf: true, ctype: "text/plain", body: `{"username":"abc"}`})
	if rec.Code != http.StatusForbidden || len(f.updates) != 0 {
		t.Fatalf("wrong content type: %d, writes %d", rec.Code, len(f.updates))
	}
}

func TestPatchMeAuthentication(t *testing.T) {
	req := httptest.NewRequest(http.MethodPatch, "/v1/me", strings.NewReader(`{"username":"abc"}`))
	req.Header.Set("Content-Type", "application/json")
	f := newPatchFake()
	rec := httptest.NewRecorder()
	New(fakeReady{}, Auth{Store: f}, NewRateLimits(1), nil, nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || len(f.updates) != 0 {
		t.Fatalf("anonymous: %d %q", rec.Code, rec.Body.String())
	}
	for _, cookie := range []bool{true, false} {
		f := newPatchFake()
		rec := doPatch(t, f, patchCall{cookie: cookie, csrf: true, suspend: true, body: `{"username":"abc"}`})
		if rec.Code != http.StatusForbidden || rec.Body.String() != "{\"error\":\"account_suspended\"}\n" || len(f.updates) != 0 {
			t.Fatalf("suspended (cookie=%v): %d %q, writes %d", cookie, rec.Code, rec.Body.String(), len(f.updates))
		}
	}
}

func TestPatchMeUniqueViolationIsConflict(t *testing.T) {
	f := newPatchFake()
	f.updateErr = &pgconn.PgError{Code: "23505", ConstraintName: "learners_username_key"}
	rec := doPatch(t, f, patchCall{cookie: true, csrf: true, body: `{"username":"taken"}`})
	if rec.Code != http.StatusConflict || rec.Body.String() != "{\"error\":\"username_taken\"}\n" {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
}

func TestPatchMeOtherDatabaseErrorIs500(t *testing.T) {
	f := newPatchFake()
	f.updateErr = &pgconn.PgError{Code: "08006"}
	rec := doPatch(t, f, patchCall{cookie: true, csrf: true, body: `{"username":"abc"}`})
	if rec.Code != http.StatusInternalServerError || rec.Body.String() != "{\"error\":\"server_error\"}\n" {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
}
