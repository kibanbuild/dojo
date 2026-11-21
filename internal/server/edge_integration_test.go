//go:build integration

package server_test

import (
	"io"
	"net/http"
	"os"
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

func TestEdgeUnknownPathIsNotFound(t *testing.T) {
	if code, _ := do(t, http.MethodGet, edgeURL(t)+"/nope"); code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", code)
	}
}
