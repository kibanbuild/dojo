package server

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kibanyadev/dojo/internal/server/repo"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// The production limits must include the callback limiter; allowCallback admits everything without it.
func TestNewRateLimitsHasEveryLimiter(t *testing.T) {
	l := NewRateLimits(1)
	if l.Auth == nil || l.Telemetry == nil || l.TelemetryAuth == nil || l.Callback == nil {
		t.Fatalf("missing limiter: %+v", l)
	}
}

func TestParseTrustedHops(t *testing.T) {
	for in, want := range map[string]int{"1": 1, " 2 ": 2, "8": 8} {
		if got, err := ParseTrustedHops(in); err != nil || got != want {
			t.Errorf("%q: got %d %v", in, got, err)
		}
	}
	for _, in := range []string{"", "0", "-1", "9", "x", "1.5"} {
		if _, err := ParseTrustedHops(in); err == nil {
			t.Errorf("%q: want an error", in)
		}
	}
}

func TestClientKey(t *testing.T) {
	tests := []struct {
		name   string
		hops   int
		xff    []string
		remote string
		want   string
	}{
		{"one hop takes the last entry", 1, []string{"9.9.9.9, 1.1.1.1"}, "10.0.0.1:1", "1.1.1.1"},
		{"spoofed left entries ignored", 1, []string{"6.6.6.6, 7.7.7.7, 1.1.1.1"}, "10.0.0.1:1", "1.1.1.1"},
		{"two hops take the second from the right", 2, []string{"6.6.6.6, 1.1.1.1, 10.1.1.1"}, "10.0.0.1:1", "1.1.1.1"},
		{"header shorter than hops uses the connection", 2, []string{"1.1.1.1"}, "10.0.0.1:1", "10.0.0.1"},
		{"no header uses the connection", 1, nil, "10.0.0.1:1", "10.0.0.1"},
		{"several header lines are one ordered list", 2, []string{"6.6.6.6", "1.1.1.1", "10.1.1.1"}, "10.0.0.1:1", "1.1.1.1"},
		{"spaces", 1, []string{"  1.1.1.1  "}, "10.0.0.1:1", "1.1.1.1"},
		{"port is dropped", 1, []string{"1.1.1.1:5555"}, "10.0.0.1:1", "1.1.1.1"},
		{"ipv6 keyed by its /64", 1, []string{"2001:db8:1:2:aaaa:bbbb:cccc:dddd"}, "10.0.0.1:1", "2001:db8:1:2::/64"},
		{"ipv6 with brackets and port", 1, []string{"[2001:db8:1:2::5]:443"}, "10.0.0.1:1", "2001:db8:1:2::/64"},
		{"same /64 shares a key", 1, []string{"2001:db8:1:2:ffff::1"}, "10.0.0.1:1", "2001:db8:1:2::/64"},
		{"ipv4-mapped ipv6 is the ipv4", 1, []string{"::ffff:1.1.1.1"}, "10.0.0.1:1", "1.1.1.1"},
		{"garbage at the trusted position falls back to the connection", 1, []string{"1.1.1.1, not-an-ip"}, "10.0.0.1:1", "10.0.0.1"},
		{"empty entry at the trusted position", 1, []string{"1.1.1.1,"}, "10.0.0.1:1", "10.0.0.1"},
		{"garbage left of the trusted position is irrelevant", 1, []string{"zzz, 1.1.1.1"}, "10.0.0.1:1", "1.1.1.1"},
		{"unparsable connection address is one shared key", 1, nil, "@", "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tt.remote
			for _, v := range tt.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			if got := clientKey(r, tt.hops); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMemLimiterBucket(t *testing.T) {
	c := &clock{t: time.Unix(1000, 0)}
	l := newMemLimiter(3, time.Minute, 10, c.now) // 1 token per 20s
	for i := 0; i < 3; i++ {
		if ok, _ := l.Allow("a"); !ok {
			t.Fatalf("request %d refused inside the burst", i)
		}
	}
	ok, retry := l.Allow("a")
	if ok || retry != 20*time.Second {
		t.Fatalf("got %v %v, want refused with 20s", ok, retry)
	}
	if ok, _ := l.Allow("b"); !ok {
		t.Fatal("another key shares the budget")
	}
	c.add(20 * time.Second)
	if ok, _ := l.Allow("a"); !ok {
		t.Fatal("no refill after 20s")
	}
	if ok, _ := l.Allow("a"); ok {
		t.Fatal("refilled more than one token")
	}
}

func TestMemLimiterParallelBurstIsExact(t *testing.T) {
	c := &clock{t: time.Unix(1000, 0)}
	l := newMemLimiter(10, time.Minute, 10, c.now)
	var admitted atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, _ := l.Allow("k"); ok {
				admitted.Add(1)
			}
		}()
	}
	wg.Wait()
	if admitted.Load() != 10 {
		t.Fatalf("admitted %d, want exactly 10", admitted.Load())
	}
	if tok := l.buckets["k"].tokens; tok < 0 || tok >= 1 {
		t.Fatalf("tokens = %v, refused requests must not spend", tok)
	}
}

func TestMemLimiterSweepsIdleKeys(t *testing.T) {
	c := &clock{t: time.Unix(1000, 0)}
	l := newMemLimiter(2, time.Minute, 10, c.now)
	for _, k := range []string{"a", "b", "c"} {
		l.Allow(k)
	}
	c.add(2 * time.Minute) // every bucket has refilled: all idle
	l.Allow("d")
	if len(l.buckets) != 1 {
		t.Fatalf("buckets = %d, want 1 after the sweep", len(l.buckets))
	}
}

func TestMemLimiterAtCapAdmitsNewKeyAndEvictsStalest(t *testing.T) {
	c := &clock{t: time.Unix(1000, 0)}
	l := newMemLimiter(5, time.Minute, 3, c.now)
	for _, k := range []string{"a", "b", "c"} { // all active, a is the stalest
		l.Allow(k)
		c.add(time.Millisecond)
	}
	if ok, _ := l.Allow("d"); !ok {
		t.Fatal("a new key was refused because the table is full")
	}
	if _, ok := l.buckets["a"]; ok {
		t.Fatal("the stalest key was not evicted")
	}
	for _, k := range []string{"b", "c", "d"} {
		if _, ok := l.buckets[k]; !ok {
			t.Fatalf("%q was evicted", k)
		}
	}
	for i := 0; i < 100; i++ {
		l.Allow("flood" + strconv.Itoa(i))
		if len(l.buckets) > 3 {
			t.Fatalf("table grew to %d, cap is 3", len(l.buckets))
		}
	}
}

// limitedChain builds the same chain as New around a test mux with the routes PR 6 and 10a add.
func limitedChain(auth Auth, rl *RateLimits, calls *atomic.Int64) http.Handler {
	mux := http.NewServeMux()
	h := func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(http.StatusNoContent) }
	mux.HandleFunc("GET /v1/auth/github/start", h)
	mux.HandleFunc("POST /v1/auth/cli", h)
	mux.HandleFunc("POST /v1/auth/logout", h)
	mux.HandleFunc("POST /v1/telemetry", h)
	mux.HandleFunc("GET /v1/health", h)
	return auth.Session(rl.wrap(mux, CSRF(mux)))
}

func testLimits(burst int) *RateLimits {
	c := &clock{t: time.Unix(1000, 0)}
	return &RateLimits{
		Auth:          newMemLimiter(burst, time.Minute, 100, c.now),
		Telemetry:     newMemLimiter(burst, time.Minute, 100, c.now),
		TelemetryAuth: newMemLimiter(burst, time.Minute, 100, c.now),
		Write:         newMemLimiter(burst, time.Minute, 100, c.now),
		Read:          newMemLimiter(burst, time.Minute, 100, c.now),
		Callback:      newMemLimiter(burst, time.Minute, 100, c.now),
		Hops:          1,
	}
}

func TestLimiterAppliesToTheRoutedPath(t *testing.T) {
	paths := []struct{ method, target string }{
		{"GET", "/v1/auth/github/start"},
		{"HEAD", "/v1/auth/github/start"},
		{"GET", "/v1/%61uth/github/start"},
		{"GET", "//v1/auth/github/start"},
		{"GET", "/v1/auth/../auth/github/start"},
		{"GET", "/v1/auth/github/start/"},
		{"GET", "/V1/AUTH/github/start"},
		{"GET", "/v1/auth/unrouted"},
		{"POST", "/v1/auth/cli"},
		{"POST", "/v1/auth/logout"},
		{"GET", "/v1/auth"},
	}
	for _, p := range paths {
		t.Run(p.method+" "+p.target, func(t *testing.T) {
			var calls atomic.Int64
			h := limitedChain(Auth{Store: &fakeSessions{}}, testLimits(2), &calls)
			codes := make([]int, 3)
			for i := range codes {
				u := httptest.NewRequest(p.method, p.target, nil)
				u.RemoteAddr = "1.1.1.1:1"
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, u)
				codes[i] = rec.Code
			}
			if codes[2] != http.StatusTooManyRequests {
				t.Fatalf("third request got %v, want a 429 (not limited like its routed path)", codes)
			}
			if codes[0] == http.StatusTooManyRequests || codes[1] == http.StatusTooManyRequests {
				t.Fatalf("the burst was refused early: %v", codes)
			}
		})
	}
}

func TestLimiterResponse(t *testing.T) {
	var calls atomic.Int64
	h := limitedChain(Auth{Store: &fakeSessions{}}, testLimits(1), &calls)
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("GET", "/v1/auth/github/start", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if i == 0 {
			continue
		}
		if rec.Code != 429 || rec.Body.String() != "{\"error\":\"rate_limited\"}\n" || rec.Header().Get("Retry-After") != "60" ||
			rec.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("got %d %q retry-after %q", rec.Code, rec.Body.String(), rec.Header().Get("Retry-After"))
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("handler ran %d times, want 1", calls.Load())
	}
}

func TestLimiterKeysByClientIPOnly(t *testing.T) {
	var calls atomic.Int64
	h := limitedChain(Auth{Store: &fakeSessions{}}, testLimits(1), &calls)
	send := func(xff, claimed string) int {
		req := httptest.NewRequest("GET", "/v1/auth/github/start?login="+claimed, nil)
		req.Header.Set("X-Forwarded-For", xff)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if send("6.6.6.6, 1.1.1.1", "a") != 204 || send("7.7.7.7, 1.1.1.1", "b") != 429 {
		t.Fatal("spoofed left entries or a claimed id changed the key")
	}
	if send("2.2.2.2", "a") != 204 {
		t.Fatal("another client IP shares the budget")
	}
}

func TestTelemetryLimits(t *testing.T) {
	post := func(h http.Handler, ip string, cookie bool) int {
		req := httptest.NewRequest("POST", "/v1/telemetry", nil)
		req.Header.Set("X-Forwarded-For", ip)
		if cookie {
			req.AddCookie(&http.Cookie{Name: SessionCookie, Value: goodToken})
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set(CSRFHeader, "1")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	t.Run("anonymous is limited per IP", func(t *testing.T) {
		var calls atomic.Int64
		h := limitedChain(Auth{Store: &fakeSessions{}}, testLimits(1), &calls)
		if post(h, "1.1.1.1", false) != 204 || post(h, "1.1.1.1", false) != 429 {
			t.Fatal("anonymous telemetry is not limited")
		}
		if post(h, "2.2.2.2", false) != 204 {
			t.Fatal("another IP shares the budget")
		}
	})
	t.Run("authenticated is limited per learner, whatever the IP", func(t *testing.T) {
		var calls atomic.Int64
		h := limitedChain(Auth{Store: &fakeSessions{row: activeRow(time.Now(), false)}}, testLimits(2), &calls)
		if post(h, "1.1.1.1", true) != 204 || post(h, "2.2.2.2", true) != 204 {
			t.Fatal("the burst was refused early")
		}
		if c := post(h, "3.3.3.3", true); c != 429 {
			t.Fatalf("third call from a third IP got %d, want 429 (per learner, not per IP)", c)
		}
		if c := post(h, "1.1.1.1", false); c != 204 {
			t.Fatalf("an anonymous call shares the learner's budget: %d", c)
		}
	})
}

func TestParallelRequestsAdmitExactlyTheBurst(t *testing.T) {
	var calls atomic.Int64
	h := limitedChain(Auth{Store: &fakeSessions{}}, testLimits(10), &calls)
	var wg sync.WaitGroup
	var limited atomic.Int64
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/auth/github/start", nil))
			if rec.Code == 429 {
				limited.Add(1)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 10 || limited.Load() != 190 {
		t.Fatalf("handler ran %d times, %d limited; want 10 and 190", calls.Load(), limited.Load())
	}
}

// The limiter wraps CSRF, it must not replace it (design 3.1).
func TestNewKeepsCSRFBehindTheLimiter(t *testing.T) {
	f := &fakeSessions{row: activeRow(time.Now(), false), learner: repo.Learner{Username: "ada"}}
	h := New(fakeReady{}, Auth{Store: f}, NewRateLimits(1), nil, nil)
	req := httptest.NewRequest("POST", "/v1/me", strings.NewReader("{}"))
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: goodToken})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "csrf_rejected") {
		t.Fatalf("got %d %q, want csrf_rejected", rec.Code, rec.Body.String())
	}
}
