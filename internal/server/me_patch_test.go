package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kibanyadev/dojo/internal/server/repo"
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
	if a.UsageStats.Valid {
		l.UsageStats = a.UsageStats.Bool
	}
	if a.AnalyticsConsent.Valid {
		l.AnalyticsConsent = a.AnalyticsConsent
	}
	f.learner = l
	return l, nil
}

func (f *fakeSessions) SetLearnerEmail(_ context.Context, _ pgtype.UUID, email string) (repo.Learner, bool, error) {
	if f.learner.Email.Valid {
		return repo.Learner{}, false, nil
	}
	f.emails = append(f.emails, email)
	f.learner.Email = pgtype.Text{String: email, Valid: true}
	f.learner.EmailSource = pgtype.Text{String: "learner", Valid: true}
	return f.learner, true, nil
}

func (f *fakeSessions) MarkFirstRunSeen(context.Context, pgtype.UUID) (repo.Learner, error) {
	if f.learner.FirstRunSeenAt.Valid {
		return repo.Learner{}, pgx.ErrNoRows
	}
	f.firstSeen++
	f.learner.FirstRunSeenAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	return f.learner, nil
}

func TestNormalizeEmail(t *testing.T) {
	long := strings.Repeat("a", 64)
	tests := []struct {
		in, want string
		ok       bool
	}{
		{"ada@example.com", "ada@example.com", true},
		{"Ada.Lovelace+dojo_1%x-y@Example.COM", "Ada.Lovelace+dojo_1%x-y@example.com", true},
		{"a@b.c", "a@b.c", true},
		{"a@sub.ex-ample.co.uk", "a@sub.ex-ample.co.uk", true},
		{"", "", false},
		{"a@b", "", false},
		{"@example.com", "", false},
		{"ada@", "", false},
		{"ada@@example.com", "", false},
		{"a@b@example.com", "", false},
		{"ada@example..com", "", false},
		{"ada@.example.com", "", false},
		{"ada@example.com.", "", false},
		{"ad a@example.com", "", false},
		{" ada@example.com", "", false},
		{"ada@example.com\n", "", false},
		{"ada@exa mple.com", "", false},
		{"ada@exam_ple.com", "", false},
		{"ada@exämple.com", "", false},
		{"\"ada\"@example.com", "", false},
		{"ada,bob@example.com", "", false},
		{"<ada>@example.com", "", false},
		{"ada\r\nBcc: x@y.z@example.com", "", false},
		{"adä@example.com", "", false},
		{long + "@example.com", long + "@example.com", true},
		{strings.Repeat("a", 300) + "@" + strings.Repeat("b", 20) + ".com", "", false},
	}
	for _, tt := range tests {
		got, ok := normalizeEmail(tt.in)
		if ok != tt.ok || got != tt.want {
			t.Errorf("normalizeEmail(%q) = %q, %v; want %q, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestPatchMeEmail(t *testing.T) {
	f := newPatchFake()
	rec := doPatch(t, f, patchCall{cookie: true, csrf: true, body: `{"email":"Ada@Example.COM"}`})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"email":"Ada@example.com"`) || len(f.emails) != 1 || len(f.updates) != 0 {
		t.Fatalf("got %d %q, emails %v, updates %d", rec.Code, rec.Body.String(), f.emails, len(f.updates))
	}
	rec = doPatch(t, f, patchCall{cookie: true, csrf: true, body: `{"email":"other@example.com"}`})
	if rec.Code != http.StatusConflict || rec.Body.String() != `{"error":"email_set"}`+"\n" || len(f.emails) != 1 {
		t.Fatalf("second: %d %q, emails %v", rec.Code, rec.Body.String(), f.emails)
	}
}

func TestPatchMeEmailSetWritesNothing(t *testing.T) {
	f := newPatchFake()
	f.learner.Email = pgtype.Text{String: "gh@example.com", Valid: true}
	rec := doPatch(t, f, patchCall{cookie: true, csrf: true, body: `{"language":"es","email":"new@example.com"}`})
	if rec.Code != http.StatusConflict || len(f.updates) != 0 || len(f.emails) != 0 {
		t.Fatalf("got %d %q, updates %d, emails %v", rec.Code, rec.Body.String(), len(f.updates), f.emails)
	}
}

func TestPatchMeEmailWithProfile(t *testing.T) {
	f := newPatchFake()
	rec := doPatch(t, f, patchCall{cookie: true, csrf: true, body: `{"language":"es","email":"ada@example.com"}`})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"language":"es"`) || !strings.Contains(rec.Body.String(), `"email":"ada@example.com"`) {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
}

func TestPatchMeFirstRunSeen(t *testing.T) {
	f := newPatchFake()
	for i := 0; i < 2; i++ {
		rec := doPatch(t, f, patchCall{cookie: true, csrf: true, body: `{"first_run_seen":true}`})
		if rec.Code != http.StatusOK {
			t.Fatalf("call %d: %d %q", i, rec.Code, rec.Body.String())
		}
	}
	if f.firstSeen != 1 || len(f.updates) != 0 {
		t.Fatalf("changed %d times, profile updates %d", f.firstSeen, len(f.updates))
	}
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
		{"only nulls", `{"username":null,"language":null,"usage_stats":null}`, "bad_request"},
		{"analytics_consent only null", `{"analytics_consent":null}`, "bad_request"},
		{"analytics_consent string", `{"analytics_consent":"true"}`, "bad_request"},
		{"analytics_consent number", `{"analytics_consent":1}`, "bad_request"},
		{"analytics_consent object", `{"analytics_consent":{}}`, "bad_request"},
		{"usage_stats string", `{"usage_stats":"false"}`, "bad_request"},
		{"usage_stats number", `{"usage_stats":0}`, "bad_request"},
		{"usage_stats object", `{"usage_stats":{}}`, "bad_request"},
		{"not json", `nope`, "bad_request"},
		{"unknown field", `{"username":"abc","nickname":"x"}`, "bad_request"},
		{"first_run_seen false", `{"first_run_seen":false}`, "bad_request"},
		{"first_run_seen string", `{"first_run_seen":"true"}`, "bad_request"},
		{"email number", `{"email":5}`, "bad_request"},
		{"email empty", `{"email":""}`, "invalid_email"},
		{"email invalid beside a valid field", `{"language":"es","email":"nope"}`, "invalid_email"},
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

func TestPatchMeUsageStats(t *testing.T) {
	for _, mode := range []struct {
		name   string
		cookie bool
	}{{"cookie", true}, {"bearer", false}} {
		for _, v := range []bool{true, false} {
			t.Run(mode.name+"/"+strconv.FormatBool(v), func(t *testing.T) {
				f := newPatchFake()
				f.learner.UsageStats = !v
				rec := doPatch(t, f, patchCall{cookie: mode.cookie, csrf: true, body: `{"usage_stats":` + strconv.FormatBool(v) + `}`})
				if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"usage_stats":`+strconv.FormatBool(v)) {
					t.Fatalf("got %d %q", rec.Code, rec.Body.String())
				}
				if len(f.updates) != 1 || f.updates[0].UsageStats != (pgtype.Bool{Bool: v, Valid: true}) || f.updates[0].Username.Valid || f.updates[0].Language.Valid {
					t.Fatalf("update args %+v", f.updates)
				}
			})
		}
	}
}

func TestPatchMeAnalyticsConsent(t *testing.T) {
	for _, mode := range []struct {
		name   string
		cookie bool
	}{{"cookie", true}, {"bearer", false}} {
		for _, v := range []bool{true, false} {
			t.Run(mode.name+"/"+strconv.FormatBool(v), func(t *testing.T) {
				f := newPatchFake()
				rec := doPatch(t, f, patchCall{cookie: mode.cookie, csrf: true, body: `{"analytics_consent":` + strconv.FormatBool(v) + `}`})
				if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"analytics_consent":`+strconv.FormatBool(v)) {
					t.Fatalf("got %d %q", rec.Code, rec.Body.String())
				}
				if len(f.updates) != 1 || f.updates[0].AnalyticsConsent != (pgtype.Bool{Bool: v, Valid: true}) ||
					f.updates[0].Username.Valid || f.updates[0].Language.Valid || f.updates[0].UsageStats.Valid {
					t.Fatalf("update args %+v", f.updates)
				}
			})
		}
	}
	t.Run("null beside a real field leaves consent alone", func(t *testing.T) {
		f := newPatchFake()
		rec := doPatch(t, f, patchCall{cookie: true, csrf: true, body: `{"language":"es","analytics_consent":null}`})
		if rec.Code != http.StatusOK || len(f.updates) != 1 || f.updates[0].AnalyticsConsent.Valid {
			t.Fatalf("got %d, args %+v", rec.Code, f.updates)
		}
	})
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

func TestMeResponseCarriesGithubID(t *testing.T) {
	b, err := json.Marshal(newMeResponse(repo.Learner{GithubID: 4242, GithubLogin: "ada"}))
	if err != nil || !strings.Contains(string(b), `"github_id":4242`) {
		t.Fatalf("%s %v", b, err)
	}
}
