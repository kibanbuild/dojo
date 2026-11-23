package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kibanbuild/dojo/internal/server/repo"
)

func (f *fakeSessions) InsertTelemetryEvents(_ context.Context, a repo.InsertTelemetryEventsParams) error {
	if f.telemetryErr != nil {
		return f.telemetryErr
	}
	f.telemetry = append(f.telemetry, a)
	return nil
}

const okTelemetry = `{"cli_version":"v0.3.1","os":"darwin/arm64","events":[{"command":"check","result":"ok","duration_ms":120}]}`

type telCall struct {
	auth  string // "", "cookie" or "bearer"
	csrf  bool
	ctype string // "" = application/json, "-" = none
	body  string
	susp  bool
}

func doTelemetry(t *testing.T, f *fakeSessions, c telCall) *httptest.ResponseRecorder {
	t.Helper()
	body := c.body
	if body == "" {
		body = okTelemetry
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/telemetry", strings.NewReader(body))
	switch c.ctype {
	case "":
		req.Header.Set("Content-Type", "application/json")
	case "-":
	default:
		req.Header.Set("Content-Type", c.ctype)
	}
	switch c.auth {
	case "cookie":
		f.row = activeRow(time.Now(), c.susp)
		req.AddCookie(&http.Cookie{Name: SessionCookie, Value: goodToken})
		if c.csrf {
			req.Header.Set(CSRFHeader, "1")
		}
	case "bearer":
		f.cli = activeCLI(time.Now(), c.susp)
		req.Header.Set("Authorization", "Bearer "+goodCLIToken)
	}
	rec := httptest.NewRecorder()
	New(fakeReady{}, Auth{Store: f}, NewRateLimits(1), nil, nil).ServeHTTP(rec, req)
	return rec
}

func newTelFake() *fakeSessions {
	return &fakeSessions{learner: repo.Learner{
		ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, OrgID: pgtype.UUID{Bytes: [16]byte{9}, Valid: true}, UsageStats: true,
	}}
}

func TestTelemetryLinking(t *testing.T) {
	tests := []struct {
		name string
		call telCall
		link bool
	}{
		{"anonymous has no learner", telCall{}, false},
		{"cookie links the learner", telCall{auth: "cookie", csrf: true}, true},
		{"bearer links the learner", telCall{auth: "bearer"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newTelFake()
			rec := doTelemetry(t, f, tt.call)
			if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
				t.Fatalf("got %d %q, want an empty 204", rec.Code, rec.Body.String())
			}
			if len(f.telemetry) != 1 {
				t.Fatalf("stored %d batches, want 1", len(f.telemetry))
			}
			got := f.telemetry[0]
			if got.LearnerID.Valid != tt.link || got.OrgID.Valid != tt.link || (tt.link && got.LearnerID != f.learner.ID) {
				t.Fatalf("learner %v org %v, link = %v", got.LearnerID, got.OrgID, tt.link)
			}
			if got.CliVersion != "v0.3.1" || got.Os != "darwin/arm64" ||
				string(got.Events) != `[{"command":"check","result":"ok","duration_ms":120}]` {
				t.Fatalf("stored %+v %s", got, got.Events)
			}
		})
	}
}

func TestTelemetryUsageStatsOffStoresNothing(t *testing.T) {
	for _, auth := range []string{"cookie", "bearer"} {
		t.Run(auth, func(t *testing.T) {
			f := newTelFake()
			f.learner.UsageStats = false
			rec := doTelemetry(t, f, telCall{auth: auth, csrf: true})
			if rec.Code != http.StatusNoContent || len(f.telemetry) != 0 {
				t.Fatalf("got %d, stored %d batches; want 204 and none", rec.Code, len(f.telemetry))
			}
		})
	}
}

func TestTelemetrySuspendedLearnerStoresNothing(t *testing.T) {
	f := newTelFake()
	if rec := doTelemetry(t, f, telCall{auth: "bearer", susp: true}); rec.Code != http.StatusNoContent || len(f.telemetry) != 0 {
		t.Fatalf("got %d, stored %d", rec.Code, len(f.telemetry))
	}
}

func TestTelemetryCSRFForCookieOnly(t *testing.T) {
	f := newTelFake()
	if rec := doTelemetry(t, f, telCall{auth: "cookie"}); rec.Code != http.StatusForbidden || len(f.telemetry) != 0 {
		t.Fatalf("cookie without the CSRF header: %d, stored %d", rec.Code, len(f.telemetry))
	}
	if rec := doTelemetry(t, f, telCall{auth: "cookie", csrf: true, ctype: "text/plain"}); rec.Code != http.StatusForbidden {
		t.Fatalf("cookie with a non-JSON type: %d", rec.Code)
	}
	if rec := doTelemetry(t, f, telCall{auth: "bearer"}); rec.Code != http.StatusNoContent {
		t.Fatalf("bearer needs no CSRF header: %d", rec.Code)
	}
	if rec := doTelemetry(t, f, telCall{}); rec.Code != http.StatusNoContent {
		t.Fatalf("anonymous needs no CSRF header: %d", rec.Code)
	}
}

func TestTelemetryBadCredentialIsRefused(t *testing.T) {
	f := newTelFake()
	req := httptest.NewRequest(http.MethodPost, "/v1/telemetry", strings.NewReader(okTelemetry))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer nope")
	rec := httptest.NewRecorder()
	New(fakeReady{}, Auth{Store: f}, NewRateLimits(1), nil, nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || len(f.telemetry) != 0 {
		t.Fatalf("got %d, stored %d; a bad token is never anonymous", rec.Code, len(f.telemetry))
	}
}

func TestTelemetryValidation(t *testing.T) {
	ev := func(n int) string {
		one := `{"command":"check","result":"ok","duration_ms":1}`
		return strings.TrimSuffix(strings.Repeat(one+",", n), ",")
	}
	wrap := func(events string) string {
		return `{"cli_version":"v1","os":"linux/amd64","events":[` + events + `]}`
	}
	tests := []struct{ name, body, ctype string }{
		{"not json", `nope`, ""},
		{"empty body", `-`, ""},
		{"wrong content type", okTelemetry, "text/plain"},
		{"no content type", okTelemetry, "-"},
		{"no events", wrap(``), ""},
		{"events null", `{"cli_version":"v1","os":"linux","events":null}`, ""},
		{"too many events", wrap(ev(maxTelemetryEvents + 1)), ""},
		{"body over the cap", wrap(ev(5)) + strings.Repeat(" ", maxTelemetryBody), ""},
		{"trailing data", okTelemetry + `{}`, ""},
		{"unknown top field", `{"cli_version":"v1","os":"linux","ip":"1.2.3.4","events":[{"command":"a","result":"ok","duration_ms":1}]}`, ""},
		{"unknown event field", wrap(`{"command":"a","result":"ok","duration_ms":1,"args":"x"}`), ""},
		{"missing duration", wrap(`{"command":"a","result":"ok"}`), ""},
		{"negative duration", wrap(`{"command":"a","result":"ok","duration_ms":-1}`), ""},
		{"duration over a day", wrap(`{"command":"a","result":"ok","duration_ms":86400001}`), ""},
		{"fractional duration", wrap(`{"command":"a","result":"ok","duration_ms":1.5}`), ""},
		{"empty command", wrap(`{"command":"","result":"ok","duration_ms":1}`), ""},
		{"uppercase command", wrap(`{"command":"Check","result":"ok","duration_ms":1}`), ""},
		{"command with a path", wrap(`{"command":"cat /etc/passwd","result":"ok","duration_ms":1}`), ""},
		{"command too long", wrap(`{"command":"a` + strings.Repeat("b", 32) + `","result":"ok","duration_ms":1}`), ""},
		{"result with a newline", wrap(`{"command":"a","result":"ok\n","duration_ms":1}`), ""},
		{"empty os", `{"cli_version":"v1","os":"","events":[{"command":"a","result":"ok","duration_ms":1}]}`, ""},
		{"bad version", `{"cli_version":"v 1","os":"linux","events":[{"command":"a","result":"ok","duration_ms":1}]}`, ""},
		{"os too long", `{"cli_version":"v1","os":"` + strings.Repeat("a", 33) + `","events":[{"command":"a","result":"ok","duration_ms":1}]}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newTelFake()
			body := tt.body
			if body == "-" {
				body = " "
			}
			rec := doTelemetry(t, f, telCall{body: body, ctype: tt.ctype})
			if rec.Code != http.StatusBadRequest || rec.Body.String() != "{\"error\":\"bad_request\"}\n" || len(f.telemetry) != 0 {
				t.Fatalf("got %d %q, stored %d", rec.Code, rec.Body.String(), len(f.telemetry))
			}
		})
	}
}

func TestTelemetryAcceptsALimitBatch(t *testing.T) {
	one := `{"command":"ssh-config","result":"not_found","duration_ms":86400000}`
	body := `{"cli_version":"v0.3.1+abc","os":"linux/amd64","events":[` + strings.TrimSuffix(strings.Repeat(one+",", maxTelemetryEvents), ",") + `]}`
	f := newTelFake()
	if rec := doTelemetry(t, f, telCall{body: body}); rec.Code != http.StatusNoContent || len(f.telemetry) != 1 {
		t.Fatalf("got %d, stored %d", rec.Code, len(f.telemetry))
	}
}

func TestTelemetryDatabaseErrors(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*fakeSessions)
		auth  string
		want  int
	}{
		{"insert fails", func(f *fakeSessions) { f.telemetryErr = errors.New("db down") }, "", 500},
		{"learner lookup fails", func(f *fakeSessions) { f.learnerErr = errors.New("db down") }, "bearer", 500},
		{"learner deleted since the lookup", func(f *fakeSessions) { f.learnerErr = pgx.ErrNoRows }, "bearer", 401},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newTelFake()
			tt.setup(f)
			rec := doTelemetry(t, f, telCall{auth: tt.auth})
			if rec.Code != tt.want || len(f.telemetry) != 0 {
				t.Fatalf("got %d %q, stored %d; want %d", rec.Code, rec.Body.String(), len(f.telemetry), tt.want)
			}
		})
	}
}
