package ghread_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kibanbuild/dojo/internal/ghread"
)

func newClient(t *testing.T, h http.HandlerFunc, cfg ghread.Config) (*ghread.Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	cfg.BaseURL = srv.URL
	c, err := ghread.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c, srv
}

func TestEndpoints(t *testing.T) {
	var gotAuth, gotUA atomic.Value
	paths := make(chan string, 10)
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		gotUA.Store(r.Header.Get("User-Agent"))
		paths <- r.URL.Path
		switch r.URL.Path {
		case "/user/1001":
			_, _ = w.Write([]byte(`{"id":1001,"login":"ada"}`))
		case "/users/ada/keys", "/users/ada/ssh_signing_keys":
			_, _ = w.Write([]byte(`[{"id":1,"key":"ssh-ed25519 AAAA"}]`))
		case "/repos/ada/dojo":
			_, _ = w.Write([]byte(`{"name":"dojo","full_name":"ada/dojo","private":false,"default_branch":"main"}`))
		case "/repos/ada/dojo/rules/branches/main":
			_, _ = w.Write([]byte(`[{"type":"deletion","ruleset_id":3}]`))
		case "/repos/ada/dojo/contents/docs/AGENTS.md":
			_, _ = w.Write([]byte(`{"type":"file","name":"AGENTS.md","path":"docs/AGENTS.md","sha":"abc"}`))
		default:
			http.NotFound(w, r)
		}
	}, ghread.Config{})
	ctx := context.Background()

	if u, err := c.UserByID(ctx, 1001); err != nil || u.Login != "ada" || u.ID != 1001 {
		t.Errorf("user: %+v %v", u, err)
	}
	if k, err := c.Keys(ctx, "ada"); err != nil || len(k) != 1 || k[0].Key != "ssh-ed25519 AAAA" {
		t.Errorf("keys: %+v %v", k, err)
	}
	if k, err := c.SigningKeys(ctx, "ada"); err != nil || len(k) != 1 {
		t.Errorf("signing: %+v %v", k, err)
	}
	if r, err := c.Repo(ctx, "ada", "dojo"); err != nil || r.Private || r.DefaultBranch != "main" {
		t.Errorf("repo: %+v %v", r, err)
	}
	if r, err := c.BranchRules(ctx, "ada", "dojo", "main"); err != nil || len(r) != 1 || r[0].Type != "deletion" {
		t.Errorf("rules: %+v %v", r, err)
	}
	if f, err := c.Contents(ctx, "ada", "dojo", "docs/AGENTS.md"); err != nil || f.SHA != "abc" {
		t.Errorf("contents: %+v %v", f, err)
	}
	if got := len(paths); got != 6 {
		t.Errorf("requests: %d", got)
	}
	if gotAuth.Load() != "" || gotUA.Load() != "dojo-cli" {
		t.Errorf("headers: auth %q ua %q", gotAuth.Load(), gotUA.Load())
	}
}

func TestOutcomes(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	cases := []struct {
		name    string
		h       http.HandlerFunc
		want    error
		contain string
	}{
		{"404", func(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) }, ghread.ErrNotFound, ""},
		{"rate limited 403", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(now.Add(12*time.Minute+time.Second).Unix(), 10))
			w.WriteHeader(403)
		}, ghread.ErrUnknown, "try again in 13 minutes"},
		{"rate limited in under a minute", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(now.Add(20*time.Second).Unix(), 10))
			w.WriteHeader(403)
		}, ghread.ErrUnknown, "try again in 1 minute"},
		{"rate limited 429", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.WriteHeader(429)
		}, ghread.ErrUnknown, "limiting requests"},
		{"403 without the header", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(403) }, ghread.ErrUnknown, "403"},
		{"5xx", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }, ghread.ErrUnknown, "503"},
		{"bad JSON", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("{")) }, ghread.ErrUnknown, "could not read"},
		{"redirect off host", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://127.0.0.2:1/steal", http.StatusFound)
		}, ghread.ErrUnknown, "302"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newClient(t, tc.h, ghread.Config{Now: func() time.Time { return now }})
			_, err := c.Repo(context.Background(), "ada", "dojo")
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if tc.contain != "" && !strings.Contains(err.Error(), tc.contain) {
				t.Errorf("message %q lacks %q", err, tc.contain)
			}
			if strings.HasSuffix(tc.contain, "1 minute") && !strings.HasSuffix(err.Error(), "1 minute") {
				t.Errorf("message %q must end in the singular", err)
			}
			var ue *ghread.UnknownError
			if errors.Is(tc.want, ghread.ErrUnknown) && errors.As(err, &ue) && ue.Reason == "" {
				t.Error("empty reason")
			}
		})
	}
}

func TestSameHostRedirectFollowed(t *testing.T) {
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/ada/dojo" {
			http.Redirect(w, r, "/repositories/9", http.StatusMovedPermanently)
			return
		}
		_, _ = w.Write([]byte(`{"name":"dojo"}`))
	}, ghread.Config{})
	if r, err := c.Repo(context.Background(), "ada", "dojo"); err != nil || r.Name != "dojo" {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestSlashBranchAllowed(t *testing.T) {
	got := make(chan string, 1)
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		got <- r.URL.Path
		_, _ = w.Write([]byte(`[]`))
	}, ghread.Config{})
	if _, err := c.BranchRules(context.Background(), "ada", "dojo", "feature/x"); err != nil {
		t.Fatal(err)
	}
	if p := <-got; p != "/repos/ada/dojo/rules/branches/feature/x" {
		t.Errorf("path %q", p)
	}
}

func TestRedirectsCostBudget(t *testing.T) {
	var calls atomic.Int32
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Redirect(w, r, "/next", http.StatusFound)
	}, ghread.Config{Budget: 2})
	// One call plus one followed hop spends the budget; the second hop is not followed.
	if _, err := c.Repo(context.Background(), "ada", "dojo"); !errors.Is(err, ghread.ErrUnknown) {
		t.Fatalf("got %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("server saw %d requests", calls.Load())
	}
	if _, err := c.Repo(context.Background(), "ada", "dojo"); !errors.Is(err, ghread.ErrUnknown) || calls.Load() != 2 {
		t.Errorf("budget should be spent: %v, %d requests", err, calls.Load())
	}
}

func TestTimeoutIsUnknown(t *testing.T) {
	release := make(chan struct{})
	c, srv := newClient(t, func(http.ResponseWriter, *http.Request) { <-release }, ghread.Config{Timeout: 50 * time.Millisecond})
	defer close(release)
	_ = srv
	start := time.Now()
	_, err := c.Repo(context.Background(), "ada", "dojo")
	if !errors.Is(err, ghread.ErrUnknown) || time.Since(start) > 2*time.Second {
		t.Fatalf("got %v after %v", err, time.Since(start))
	}
}

func TestUnreachableIsUnknown(t *testing.T) {
	c, err := ghread.New(ghread.Config{BaseURL: "http://127.0.0.1:1", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Keys(context.Background(), "ada"); !errors.Is(err, ghread.ErrUnknown) {
		t.Fatalf("got %v", err)
	}
}

func TestBudgetExhaustedIsUnknown(t *testing.T) {
	var calls atomic.Int32
	c, _ := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`[]`))
	}, ghread.Config{Budget: 2})
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := c.Keys(ctx, "ada"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.Keys(ctx, "ada"); !errors.Is(err, ghread.ErrUnknown) {
		t.Fatalf("third call: %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("server saw %d calls", calls.Load())
	}
}

func TestDefaultBudgetIsSeven(t *testing.T) {
	c, _ := newClient(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`[]`)) }, ghread.Config{})
	n := 0
	for {
		if _, err := c.Keys(context.Background(), "ada"); err != nil {
			break
		}
		n++
	}
	if n != 7 {
		t.Fatalf("allowed %d calls", n)
	}
}

func TestInvalidInputRefusedWithoutACall(t *testing.T) {
	var calls atomic.Int32
	c, _ := newClient(t, func(w http.ResponseWriter, _ *http.Request) { calls.Add(1) }, ghread.Config{Budget: 1})
	ctx := context.Background()
	for _, login := range []string{"", "-ada", "a/b", "ada?x=1", "a..", "ada%2f", "../x", "a b", "this-login-is-way-too-long-to-be-a-github-login-ok"} {
		if _, err := c.Keys(ctx, login); !errors.Is(err, ghread.ErrInvalid) {
			t.Errorf("login %q: %v", login, err)
		}
	}
	if _, err := c.Repo(ctx, "ada", ".."); !errors.Is(err, ghread.ErrInvalid) {
		t.Errorf("repo ..: %v", err)
	}
	for _, b := range []string{"", ".", "..", "a/./b", "a/../b", "/main", "main/", "a//b", "a b", "a?x", strings.Repeat("a", 101)} {
		if _, err := c.BranchRules(ctx, "ada", "dojo", b); !errors.Is(err, ghread.ErrInvalid) {
			t.Errorf("branch %q: %v", b, err)
		}
	}
	if _, err := c.Contents(ctx, "ada", "dojo", "a/../b"); !errors.Is(err, ghread.ErrInvalid) {
		t.Errorf("path: %v", err)
	}
	if _, err := c.UserByID(ctx, 0); !errors.Is(err, ghread.ErrInvalid) {
		t.Errorf("id: %v", err)
	}
	if calls.Load() != 0 {
		t.Errorf("invalid input reached the server %d times", calls.Load())
	}
	if _, err := c.Keys(ctx, "z"); err == nil || errors.Is(err, ghread.ErrInvalid) {
		// a one-character login is valid; the budget of 1 is still unspent, so the call is made (empty body)
		t.Errorf("single-character login: %v", err)
	}
}

func TestLoadConfig(t *testing.T) {
	env := func(v string) func(string) string { return func(string) string { return v } }
	for v, ok := range map[string]bool{
		"": true, "https://api.github.com": true, "https://api.github.com/": true, "http://127.0.0.1:8081": true,
		"http://localhost:8081": true, "http://api.github.com": false, "http://fake-github:8081": false,
		"https://u@host": false, "https://host/path": false, "https://host?x": false, "ftp://host": false, "https://host:0": false,
	} {
		cfg, err := ghread.LoadConfig(env(v))
		if (err == nil) != ok {
			t.Errorf("%q: err %v", v, err)
		}
		if v == "" && cfg.BaseURL != ghread.DefaultAPIURL {
			t.Errorf("default: %q", cfg.BaseURL)
		}
	}
}
