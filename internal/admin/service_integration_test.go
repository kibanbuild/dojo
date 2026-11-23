//go:build integration

package admin_test

import (
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/kibanbuild/dojo/internal/admin"
)

func adminURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("DOJO_ADMIN_URL")
	if u == "" {
		t.Fatal("DOJO_ADMIN_URL is not set: run `make test-integration`")
	}
	return u
}

func get(t *testing.T, host string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, adminURL(t)+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(b)
}

// The right Host reaches the service and its database connection (as dojo_admin).
func TestServiceAnswersOnItsHost(t *testing.T) {
	if code, body := get(t, admin.Host); code != http.StatusOK || body != "{\"status\":\"ok\"}\n" {
		t.Fatalf("got %d %q", code, body)
	}
}

func TestServiceRefusesOtherHosts(t *testing.T) {
	for _, host := range []string{"localhost:8090", "127.0.0.1:8080", "evil.example.com"} {
		if code, body := get(t, host); code != http.StatusMisdirectedRequest || body != "" {
			t.Errorf("%s: got %d %q", host, code, body)
		}
	}
}
