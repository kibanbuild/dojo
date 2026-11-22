//go:build integration

package server_test

import (
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func edgeURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("DOJO_EDGE_URL")
	if u == "" {
		t.Fatal("DOJO_EDGE_URL is not set: run `make test-integration`")
	}
	return u
}

func TestEdgeServesTheSite(t *testing.T) {
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Get(edgeURL(t) + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("got %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if !strings.Contains(string(b), "Dojo, coming soon") {
		t.Fatalf("hello text missing in %q", b)
	}
}

func TestEdgeProxiesTheAPI(t *testing.T) {
	code, body := do(t, http.MethodGet, edgeURL(t)+"/v1/health")
	if code != http.StatusOK || body != "{\"status\":\"ok\"}\n" {
		t.Fatalf("got %d %q", code, body)
	}
}

// Caddy replaces a client-sent X-Forwarded-For with the peer address, so a spoofed value must not
// give a fresh budget: the 429 arrives at the same request as without it.
func TestEdgeIgnoresSpoofedForwardedFor(t *testing.T) {
	const burst = 30 // authBurst
	limitedAt := 0
	for i := 1; i <= burst+5; i++ {
		req, err := http.NewRequest(http.MethodGet, edgeURL(t)+"/v1/auth/rate-limit-probe", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Forwarded-For", "10.9.8."+strconv.Itoa(i))
		resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			if resp.Header.Get("Retry-After") == "" {
				t.Error("429 without Retry-After")
			}
			limitedAt = i
			break
		}
	}
	if limitedAt == 0 || limitedAt > burst+1 {
		t.Fatalf("first 429 at request %d, want within %d (a spoofed address changed the key)", limitedAt, burst+1)
	}
}

func TestEdgeUnknownPathIsNotFound(t *testing.T) {
	if code, _ := do(t, http.MethodGet, edgeURL(t)+"/nope"); code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", code)
	}
}

// The operator service is never on the public origin: these must be a real 404, not the site.
func TestEdgeOperatorPathsAreNotFound(t *testing.T) {
	for _, p := range []string{"/admin", "/admin/", "/admin/learners", "/ADMIN", "/Admin/x", "/admin.html", "/administrator", "//admin", "/review", "/review/", "/review/queue"} {
		if code, body := do(t, http.MethodGet, edgeURL(t)+p); code != http.StatusNotFound || body != "" {
			t.Errorf("%s: got %d %q, want an empty 404", p, code, body)
		}
	}
}
