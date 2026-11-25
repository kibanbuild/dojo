package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestParseStreamHeartbeat(t *testing.T) {
	for in, want := range map[string]time.Duration{"": 25 * time.Second, "1": time.Second, "55": 55 * time.Second} {
		if got, err := ParseStreamHeartbeat(in); err != nil || got != want {
			t.Errorf("%q: got %v %v", in, got, err)
		}
	}
	for _, in := range []string{"0", "56", "-1", "x", "1.5"} {
		if _, err := ParseStreamHeartbeat(in); err == nil {
			t.Errorf("%q: want an error", in)
		}
	}
}

func streamChain(f *fakeSessions, rl *RateLimits) http.Handler {
	a := Auth{Store: f, Stream: NewHub(nil)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+streamPath, a.stream)
	return a.Session(rl.wrap(mux, CSRF(mux)))
}

func TestStreamRefusals(t *testing.T) {
	tests := []struct {
		name string
		f    *fakeSessions
		auth bool
		want int
		body string
	}{
		{"no cookie", &fakeSessions{}, false, 401, "unauthenticated"},
		{"bearer", &fakeSessions{cli: activeCLI(time.Now(), false)}, false, 403, "forbidden"},
		{"suspended", &fakeSessions{row: activeRow(time.Now(), true)}, true, 403, "account_suspended"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, streamPath, nil)
			switch {
			case tt.auth:
				req.AddCookie(&http.Cookie{Name: SessionCookie, Value: goodToken})
			case tt.f.cli != nil:
				req.Header.Set("Authorization", "Bearer "+goodCLIToken)
			}
			rec := httptest.NewRecorder()
			streamChain(tt.f, testLimits(5)).ServeHTTP(rec, req)
			if rec.Code != tt.want || !strings.Contains(rec.Body.String(), tt.body) {
				t.Fatalf("got %d %q", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestStreamConnectionRateLimit(t *testing.T) {
	rl := testLimits(2)
	rl.Stream = newMemLimiter(2, time.Minute, 100, time.Now)
	h := streamChain(&fakeSessions{row: activeRow(time.Now(), true)}, rl)
	var codes []int
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, streamPath, nil)
		req.AddCookie(&http.Cookie{Name: SessionCookie, Value: goodToken})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		codes = append(codes, rec.Code)
		if rec.Code == 429 && rec.Header().Get("Retry-After") == "" {
			t.Error("429 without Retry-After")
		}
	}
	if codes[0] != 403 || codes[1] != 403 || codes[2] != 429 {
		t.Fatalf("codes %v", codes)
	}
}

func TestUnsubscribeNeverBlocksWhileListenerIsDown(t *testing.T) {
	h := NewHub(nil) // Run is not started: nobody reads the commands
	s := &Sub{C: make(chan []byte, 1), Wake: make(chan struct{}, 1), channel: "dojo_learner_x"}
	h.subs[s.channel] = map[*Sub]struct{}{s: {}}
	start := time.Now()
	h.Unsubscribe(s)
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatalf("Unsubscribe took %v", d)
	}
	if len(h.channels()) != 0 {
		t.Fatal("sub still registered")
	}
}

func TestShutdownEndsOpenStreams(t *testing.T) {
	hub := NewHub(nil)
	go func() { // stands in for the listener loop
		for c := range hub.cmds {
			close(c.done)
		}
	}()
	h := streamChain2(&fakeSessions{row: activeRow(time.Now(), false)}, hub)
	ts := httptest.NewServer(h)
	defer ts.Close()
	ts.Config.RegisterOnShutdown(hub.Shutdown)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+streamPath, nil)
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: goodToken})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 64)
	if n, err := resp.Body.Read(buf); err != nil || !strings.Contains(string(buf[:n]), "retry: 3000") {
		t.Fatalf("first read: %q %v", buf[:n], err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	if err := ts.Config.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v after %v", err, time.Since(start))
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("shutdown took %v", d)
	}
}

func streamChain2(f *fakeSessions, hub *Hub) http.Handler {
	a := Auth{Store: f, Stream: hub}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+streamPath, a.stream)
	return a.Session(mux)
}

// A connect that never answers (a blackholed address) must not hold the listener loop for the
// OS dial timeout: every Subscribe waits behind that loop.
func TestRunGivesUpOnAConnectThatNeverAnswers(t *testing.T) {
	calls := make(chan time.Duration, 8)
	h := NewHub(func(ctx context.Context) (*pgx.Conn, error) {
		start := time.Now()
		<-ctx.Done()
		calls <- time.Since(start)
		return nil, ctx.Err()
	})
	h.connectTimeout = 50 * time.Millisecond
	h.reconnectDelay = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	for i := 0; i < 2; i++ {
		select {
		case d := <-calls:
			if d > time.Second {
				t.Fatalf("connect held for %v", d)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("attempt %d never ended: the listener loop is stuck in connect", i+1)
		}
	}
}
