package server

import (
	"math"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// Sign-in is OAuth, so there is no password to guess; the limit bounds abuse of GitHub calls and
	// token minting. Burst 30 leaves room for a class behind one school NAT.
	authBurst = 30
	authPer   = time.Minute
	// Anonymous CLI events before login.
	telemetryBurst = 60
	telemetryPer   = time.Minute
	// Authenticated CLI events, per learner across all devices: a valid token is no longer unlimited.
	telemetryAuthBurst = 120

	maxKeys    = 50_000
	sweepEvery = 30 * time.Second
	// capSweepEvery bounds how often a full table is swept before evicting.
	capSweepEvery = time.Second
)

// Limiter admits or refuses one request for a key. A refused request must change nothing.
// The in-memory implementation is per API task; a shared store can replace it behind this interface.
type Limiter interface {
	Allow(key string) (ok bool, retryAfter time.Duration)
}

type bucket struct {
	tokens float64
	last   time.Time
}

// memLimiter is a token bucket per key: burst tokens, refilled evenly over per.
type memLimiter struct {
	mu        sync.Mutex
	burst     float64
	rate      float64 // tokens per second
	max       int
	now       func() time.Time
	buckets   map[string]*bucket
	lastSweep time.Time
}

func newMemLimiter(burst int, per time.Duration, max int, now func() time.Time) *memLimiter {
	return &memLimiter{
		burst: float64(burst), rate: float64(burst) / per.Seconds(), max: max, now: now,
		buckets: make(map[string]*bucket), lastSweep: now(),
	}
}

func (l *memLimiter) Allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.Sub(l.lastSweep) >= sweepEvery {
		l.sweep(now)
	}
	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= l.max {
			l.makeRoom(now)
		}
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	} else {
		b.tokens = math.Min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
		b.last = now
	}
	if b.tokens < 1 {
		return false, time.Duration(math.Ceil((1-b.tokens)/l.rate)) * time.Second
	}
	b.tokens--
	return true, 0
}

// makeRoom frees one slot at the cap: sweep (at most once a second), else evict the stalest bucket.
// A new key is never refused for a full table; an evicted key only loses its own state.
func (l *memLimiter) makeRoom(now time.Time) {
	if now.Sub(l.lastSweep) >= capSweepEvery {
		l.sweep(now)
	}
	if len(l.buckets) < l.max {
		return
	}
	var stalest string
	var oldest time.Time
	for k, b := range l.buckets {
		if stalest == "" || b.last.Before(oldest) {
			stalest, oldest = k, b.last
		}
	}
	delete(l.buckets, stalest)
}

// sweep drops buckets that have refilled to full: they behave exactly like an absent key.
func (l *memLimiter) sweep(now time.Time) {
	l.lastSweep = now
	for k, b := range l.buckets {
		if b.tokens+now.Sub(b.last).Seconds()*l.rate >= l.burst {
			delete(l.buckets, k)
		}
	}
}

// RateLimits limits the auth routes and anonymous telemetry per client IP, and authenticated
// telemetry per learner (design 3.1).
type RateLimits struct {
	Auth, Telemetry Limiter
	// TelemetryAuth is keyed by learner id, so one learner's devices share it.
	TelemetryAuth Limiter
	// Callback is taken inside the sign-in callback, after the state cookie checks out and before any GitHub call.
	Callback Limiter
	Hops     int
}

// NewRateLimits returns the in-memory limits with the production constants.
func NewRateLimits(hops int) *RateLimits {
	return &RateLimits{
		Auth:          newMemLimiter(authBurst, authPer, maxKeys, time.Now),
		Telemetry:     newMemLimiter(telemetryBurst, telemetryPer, maxKeys, time.Now),
		TelemetryAuth: newMemLimiter(telemetryAuthBurst, telemetryPer, maxKeys, time.Now),
		Callback:      newMemLimiter(authBurst, authPer, maxKeys, time.Now),
		Hops:          hops,
	}
}

// wrap limits requests by the route the mux itself picks (mux.Handler is what ServeHTTP uses),
// unioned with the cleaned request path so an unrouted 404 under /v1/auth is limited too.
func (rl *RateLimits) wrap(mux *http.ServeMux, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pattern := mux.Handler(r)
		var lim Limiter
		key := ""
		isStart := pattern == "POST "+startPath && r.URL.Path == startPath
		switch {
		// The callback has its own limiter inside the handler, taken once the state cookie checks out.
		case pattern == "GET "+callbackPath && r.URL.Path == callbackPath:
		case matches(pattern, r, isAuthPath), pattern == "DELETE /v1/me" && path.Clean("/"+r.URL.Path) == "/v1/me":
			lim = rl.Auth
		case matches(pattern, r, isTelemetryPath):
			if p, authed := principalOf(r.Context()); authed {
				lim, key = rl.TelemetryAuth, "learner:"+p.learner.String()
			} else {
				lim = rl.Telemetry
			}
		}
		if lim != nil {
			if key == "" {
				key = clientKey(r, rl.Hops)
			}
			if ok, retry := lim.Allow(key); !ok {
				secs := max(1, int(math.Ceil(retry.Seconds())))
				w.Header().Set("Retry-After", strconv.Itoa(secs))
				if isStart { // a browser form post: show O1, not JSON
					redirectSignin(w, r, "error=rate_limited")
					return
				}
				writeError(w, http.StatusTooManyRequests, "rate_limited")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func matches(pattern string, r *http.Request, is func(string) bool) bool {
	p := pattern
	if i := strings.IndexByte(p, ' '); i >= 0 { // "GET /v1/x"
		p = p[i+1:]
	}
	return is(p) || is(path.Clean("/"+r.URL.Path))
}

func isAuthPath(p string) bool {
	p = strings.ToLower(p)
	return p == "/v1/auth" || strings.HasPrefix(p, "/v1/auth/")
}

func isTelemetryPath(p string) bool {
	return strings.TrimSuffix(strings.ToLower(p), "/") == "/v1/telemetry"
}
