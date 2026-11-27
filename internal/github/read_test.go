package github

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const sha40 = "0123456789abcdef0123456789abcdef01234567"

func readClient(t *testing.T, token string, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(Config{APIURL: srv.URL, ReadToken: Secret(token)})
}

func TestCommitParsesAnswerAndSendsToken(t *testing.T) {
	var gotPath, gotAuth string
	c := readClient(t, "tok", func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"sha":"` + sha40 + `","commit":{"message":"pass: a/b\n\nDojo-Ticket: a/b","verification":{"verified":true,"reason":"valid"}}}`))
	})
	got, err := c.Commit(context.Background(), "ada", sha40)
	if err != nil || !got.Verified || got.VerifiedReason != "valid" || !strings.Contains(got.Message, "Dojo-Ticket: a/b") {
		t.Fatalf("%+v %v", got, err)
	}
	if gotPath != "/repos/ada/dojo/commits/"+sha40 || gotAuth != "Bearer tok" {
		t.Fatalf("path %q auth %q", gotPath, gotAuth)
	}
}

func TestNoTokenMeansNoAuthorizationHeader(t *testing.T) {
	c := readClient(t, "", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("authorization sent without a token")
		}
		_, _ = w.Write([]byte(`{"id":7,"login":"ada"}`))
	})
	if u, err := c.UserByID(context.Background(), 7); err != nil || u.Login != "ada" {
		t.Fatalf("%+v %v", u, err)
	}
}

func TestCompareParsesStatus(t *testing.T) {
	var gotPath string
	c := readClient(t, "", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"status":"behind","ahead_by":0,"behind_by":2}`))
	})
	got, err := c.Compare(context.Background(), "ada", "main", sha40)
	if err != nil || got.Status != "behind" || got.BehindBy != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	if gotPath != "/repos/ada/dojo/compare/main..."+sha40 {
		t.Fatalf("path %q", gotPath)
	}
}

func TestInvalidInputsNeverReachTheNetwork(t *testing.T) {
	var calls atomic.Int32
	c := readClient(t, "tok", func(http.ResponseWriter, *http.Request) { calls.Add(1) })
	ctx := context.Background()
	for _, login := range []string{"", "a/b", "a..", "..", "a b", "a?x=1", "a#", strings.Repeat("a", 40), "é", "a%2fb"} {
		if _, err := c.Commit(ctx, login, sha40); err == nil {
			t.Errorf("login %q accepted", login)
		}
	}
	for _, sha := range []string{"", "main", strings.ToUpper(sha40), sha40[:39], sha40 + "0", "../" + sha40[3:]} {
		if _, err := c.Commit(ctx, "ada", sha); err == nil {
			t.Errorf("sha %q accepted", sha)
		}
		if _, err := c.Compare(ctx, "ada", "main", sha); err == nil {
			t.Errorf("compare head %q accepted", sha)
		}
	}
	for _, base := range []string{"", "a/b", "..", "a..b", "-x", "a?b", "a#b"} {
		if _, err := c.Compare(ctx, "ada", base, sha40); err == nil {
			t.Errorf("base %q accepted", base)
		}
	}
	if _, err := c.UserByID(ctx, 0); err == nil {
		t.Error("id 0 accepted")
	}
	if calls.Load() != 0 {
		t.Fatalf("%d requests made for invalid input", calls.Load())
	}
}

func TestPublicGetRefusesPathsOutsideTheOrigin(t *testing.T) {
	var calls atomic.Int32
	c := readClient(t, "tok", func(http.ResponseWriter, *http.Request) { calls.Add(1) })
	for _, p := range []string{"//evil.example/x", "/a/../b", "/a?x=1", "/a#f", "x", "/a/./b"} {
		if _, err := c.publicGet(context.Background(), p); err == nil {
			t.Errorf("path %q accepted", p)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("%d requests", calls.Load())
	}
}

func TestRedirectIsNotFollowedAndTokenStaysHome(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { elsewhere.Add(1) }))
	t.Cleanup(other.Close)
	c := readClient(t, "tok", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/x", http.StatusFound)
	})
	_, err := c.Commit(context.Background(), "ada", sha40)
	if !errors.Is(err, ErrUnavailable) || elsewhere.Load() != 0 {
		t.Fatalf("err %v, requests elsewhere %d", err, elsewhere.Load())
	}
}

func TestReadStatusMapping(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		headers map[string]string
		want    error
	}{
		{"not found", 404, nil, ErrNotFound},
		{"rate limited 403", 403, map[string]string{"X-RateLimit-Remaining": "0"}, ErrRateLimited},
		{"secondary limit", 403, map[string]string{"Retry-After": "60"}, ErrRateLimited},
		{"429", 429, nil, ErrRateLimited},
		{"plain 403", 403, nil, ErrUnavailable},
		{"5xx", 502, nil, ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := readClient(t, "", func(w http.ResponseWriter, _ *http.Request) {
				for k, v := range tc.headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.status)
			})
			if _, err := c.Commit(context.Background(), "ada", sha40); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestUnusableBodiesAreUnavailable(t *testing.T) {
	c := readClient(t, "", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"sha":"other"}`)) })
	if _, err := c.Commit(context.Background(), "ada", sha40); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("%v", err)
	}
	if _, err := c.UserByID(context.Background(), 7); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("%v", err)
	}
	if _, err := c.Compare(context.Background(), "ada", "main", sha40); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("%v", err)
	}
}

func TestRequireReadToken(t *testing.T) {
	for _, tc := range []struct {
		env, token string
		wantErr    bool
	}{
		{"pro", "", true}, {"pre", "", true}, {"pro", "t", false}, {"pre", "t", false},
		{"local", "", false}, {"", "", false},
	} {
		err := RequireReadToken(tc.env, Config{ReadToken: Secret(tc.token)})
		if (err != nil) != tc.wantErr {
			t.Errorf("env %q token %q: %v", tc.env, tc.token, err)
		}
	}
}

func TestLoadConfigReadsTheReadToken(t *testing.T) {
	env := map[string]string{"GITHUB_CLIENT_ID": "i", "GITHUB_CLIENT_SECRET": "s", "DOJO_GITHUB_READ_TOKEN": " ghp_x\n"}
	cfg, err := LoadConfig(func(k string) string { return env[k] }, true)
	if err != nil || string(cfg.ReadToken) != "ghp_x" {
		t.Fatalf("%v %q", err, string(cfg.ReadToken))
	}
	if strings.Contains(cfg.String(), "ghp_x") {
		t.Fatal("token printed")
	}
}
