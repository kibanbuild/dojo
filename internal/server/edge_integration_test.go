//go:build integration

package server_test

import (
	"io"
	"net/http"
	"os"
	"regexp"
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
	for _, p := range []string{"/admin", "/admin/", "/admin/learners", "/ADMIN", "/Admin/x", "/admin.html", "/administrator", "//admin", "/review", "/review/", "/review/queue", "/admin/x"} {
		if code, body := do(t, http.MethodGet, edgeURL(t)+p); code != http.StatusNotFound || body != "" {
			t.Errorf("%s: got %d %q, want an empty 404", p, code, body)
		}
	}
}

// The SPA fallback is scoped to /app. The real bite is that /app/admin and /app/review are SPA routes
// (200 index.html) while /admin and /review stay empty 404s: the operator rule is root-anchored and first.
// /reviews is the control: not an operator path, so it gets the site's 404, which has a body (the operator block's is empty).
func TestEdgeOperatorRuleIsRootAnchored(t *testing.T) {
	for _, p := range []string{"/app/admin", "/app/review"} {
		if code, body := do(t, http.MethodGet, edgeURL(t)+p); code != http.StatusOK || !strings.Contains(body, `id="root"`) {
			t.Errorf("%s: got %d, want the SPA index", p, code)
		}
	}
	if code, body := do(t, http.MethodGet, edgeURL(t)+"/reviews"); code != http.StatusNotFound || body == "" {
		t.Errorf("/reviews: got %d %q, want the site 404 with a body", code, body)
	}
}

func TestEdgeServesTheSPAWithFallback(t *testing.T) {
	client := &http.Client{Timeout: 10 * time.Second}
	get := func(p string) (*http.Response, string) {
		resp, err := client.Get(edgeURL(t) + p)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp, string(b)
	}
	var index string
	for _, p := range []string{"/app/", "/app/some/deep/link"} {
		resp, body := get(p)
		if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") || !strings.Contains(body, `id="root"`) {
			t.Fatalf("%s: got %d %q", p, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		if resp.Header.Get("Cache-Control") != "no-cache" {
			t.Errorf("%s: Cache-Control %q, want no-cache", p, resp.Header.Get("Cache-Control"))
		}
		index = body
	}
	// The bare path redirects to the base, keeping the query.
	noFollow := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for p, want := range map[string]string{"/app": "/app/?", "/app?x=1": "/app/?x=1"} {
		resp, err := noFollow.Get(edgeURL(t) + p)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusPermanentRedirect || resp.Header.Get("Location") != want {
			t.Errorf("%s: got %d Location %q, want 308 %q", p, resp.StatusCode, resp.Header.Get("Location"), want)
		}
	}
	m := regexp.MustCompile(`/app/assets/[^"]+\.js`).FindString(index)
	if m == "" {
		t.Fatalf("no hashed asset in %q", index)
	}
	resp, _ := get(m)
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
		t.Errorf("%s: got %d, Cache-Control %q", m, resp.StatusCode, resp.Header.Get("Cache-Control"))
	}
	resp, _ = get("/app/assets/missing.js")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("missing asset: got %d, want 404", resp.StatusCode)
	}
	if cc := resp.Header.Get("Cache-Control"); strings.Contains(cc, "immutable") || strings.Contains(cc, "max-age") {
		t.Errorf("missing asset 404 is cacheable: Cache-Control %q", cc)
	}
}
