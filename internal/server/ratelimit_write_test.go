package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kibanbuild/dojo/internal/server/repo"
)

// writeChain is the real wrap with stub handlers behind the real session middleware.
func writeChain(f *fakeSessions, rl *RateLimits, calls *atomic.Int64) http.Handler {
	mux := http.NewServeMux()
	h := func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(http.StatusNoContent) }
	mux.HandleFunc("PATCH /v1/me", h)
	mux.HandleFunc("GET /v1/me", h)
	mux.HandleFunc("DELETE /v1/me", h)
	mux.HandleFunc("DELETE /v1/me/devices/{id}", h)
	mux.HandleFunc("POST /v1/telemetry", h)
	return Auth{Store: f}.Session(rl.wrap(mux, CSRF(mux)))
}

func cookieReq(method, target string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(CSRFHeader, "1")
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: goodToken})
	return req
}

func TestNewRateLimitsHasWriteLimiter(t *testing.T) {
	if NewRateLimits(1).Write == nil {
		t.Fatal("Write limiter missing")
	}
}

func TestLearnerWriteLimit(t *testing.T) {
	routes := []struct{ method, target string }{
		{"PATCH", "/v1/me"},
		{"DELETE", "/v1/me/devices/abc"},
		{"DELETE", "/v1/me/devices/other"}, // same budget across ids
	}
	for _, rt := range routes {
		t.Run(rt.method+" "+rt.target, func(t *testing.T) {
			f := &fakeSessions{row: activeRow(time.Now(), false), learner: repo.Learner{Username: "ada"}}
			var calls atomic.Int64
			h := writeChain(f, testLimits(2), &calls)
			var codes []int
			var retry string
			for i := 0; i < 3; i++ {
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, cookieReq(rt.method, rt.target))
				codes = append(codes, rec.Code)
				if rec.Code == 429 {
					retry = rec.Header().Get("Retry-After")
					if !strings.Contains(rec.Body.String(), `"rate_limited"`) {
						t.Fatalf("body %q", rec.Body.String())
					}
				}
			}
			if codes[0] != 204 || codes[1] != 204 || codes[2] != 429 {
				t.Fatalf("codes %v, want 204 204 429", codes)
			}
			if retry == "" {
				t.Fatal("no Retry-After")
			}
			if calls.Load() != 2 {
				t.Fatalf("handler ran %d times, want 2", calls.Load())
			}
		})
	}
}

func TestLearnerWriteLimitSharesOneBudgetAcrossRoutes(t *testing.T) {
	f := &fakeSessions{row: activeRow(time.Now(), false), learner: repo.Learner{Username: "ada"}}
	var calls atomic.Int64
	h := writeChain(f, testLimits(2), &calls)
	for i, rt := range []struct{ m, p string }{{"PATCH", "/v1/me"}, {"DELETE", "/v1/me/devices/x"}} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, cookieReq(rt.m, rt.p))
		if rec.Code != 204 {
			t.Fatalf("call %d: %d", i, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, cookieReq("PATCH", "/v1/me"))
	if rec.Code != 429 {
		t.Fatalf("third write: %d, want 429", rec.Code)
	}
}

func TestLearnerWriteLimitDoesNotAffectOtherLearnersOrReads(t *testing.T) {
	f := &fakeSessions{row: activeRow(time.Now(), false), learner: repo.Learner{Username: "ada"}}
	var calls atomic.Int64
	rl := testLimits(1)
	h := writeChain(f, rl, &calls)
	for i := 0; i < 2; i++ {
		h.ServeHTTP(httptest.NewRecorder(), cookieReq("PATCH", "/v1/me"))
	}
	// Reads are not limited.
	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, cookieReq("GET", "/v1/me"))
		if rec.Code != 204 {
			t.Fatalf("GET: %d", rec.Code)
		}
	}
	// Another learner has their own bucket.
	f.row.LearnerID = pgtype.UUID{Bytes: [16]byte{7}, Valid: true}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, cookieReq("PATCH", "/v1/me"))
	if rec.Code != 204 {
		t.Fatalf("other learner: %d, want 204", rec.Code)
	}
}

func TestLearnerWriteLimitRefillsAndSkipsAnonymous(t *testing.T) {
	f := &fakeSessions{row: activeRow(time.Now(), false), learner: repo.Learner{Username: "ada"}}
	var calls atomic.Int64
	c := &clock{t: time.Unix(1000, 0)}
	rl := testLimits(1)
	rl.Write = newMemLimiter(1, time.Minute, 100, c.now)
	h := writeChain(f, rl, &calls)
	do := func() int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, cookieReq("PATCH", "/v1/me"))
		return rec.Code
	}
	if do() != 204 || do() != 429 {
		t.Fatal("want 204 then 429")
	}
	c.add(time.Minute)
	if do() != 204 {
		t.Fatal("bucket did not refill")
	}
	// Anonymous: no principal, no learner key, the handler decides (401 in production).
	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("PATCH", "/v1/me", nil))
		if rec.Code == 429 {
			t.Fatalf("anonymous request %d limited per learner", i)
		}
	}
}

func TestWriteLimiterEvictsAtCapWithoutRefusingNewLearners(t *testing.T) {
	c := &clock{t: time.Unix(1000, 0)}
	l := newMemLimiter(1, time.Minute, 3, c.now)
	for _, k := range []string{"a", "b", "c", "d", "e"} {
		if ok, _ := l.Allow("learner:" + k); !ok {
			t.Fatalf("new learner %s refused at the cap", k)
		}
		c.add(time.Millisecond)
	}
	if len(l.buckets) > 3 {
		t.Fatalf("%d buckets, cap 3", len(l.buckets))
	}
}

// Account deletion keeps the per-IP limit and adds the per-learner one.
func TestDeleteMeHasLearnerLimitToo(t *testing.T) {
	f := &fakeSessions{row: activeRow(time.Now(), false), learner: repo.Learner{Username: "ada"}}
	var calls atomic.Int64
	rl := testLimits(100)
	rl.Write = newMemLimiter(1, time.Minute, 100, (&clock{t: time.Unix(1000, 0)}).now)
	h := writeChain(f, rl, &calls)
	codes := []int{}
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, cookieReq("DELETE", "/v1/me"))
		codes = append(codes, rec.Code)
	}
	if codes[0] != 204 || codes[1] != 429 {
		t.Fatalf("codes %v", codes)
	}
}
