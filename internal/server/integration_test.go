//go:build integration

package server_test

import (
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func apiURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("DOJO_API_URL")
	if u == "" {
		t.Fatal("DOJO_API_URL is not set: run `make test-integration`")
	}
	return u
}

func do(t *testing.T, method, url string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
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

func TestHealthOK(t *testing.T) {
	code, body := do(t, http.MethodGet, apiURL(t)+"/v1/health")
	if code != http.StatusOK || body != "{\"status\":\"ok\"}\n" {
		t.Fatalf("got %d %q", code, body)
	}
}

func TestHealthRejectsPost(t *testing.T) {
	if code, _ := do(t, http.MethodPost, apiURL(t)+"/v1/health"); code != http.StatusMethodNotAllowed {
		t.Fatalf("got %d, want 405", code)
	}
}

func TestHealthUnavailableWhenDatabaseStops(t *testing.T) {
	url := apiURL(t)
	project := os.Getenv("DOJO_COMPOSE_PROJECT")
	if project == "" {
		t.Fatal("DOJO_COMPOSE_PROJECT is not set: run `make test-integration`")
	}
	ids, err := exec.Command("docker", "ps", "-q",
		"--filter", "label=com.docker.compose.project="+project,
		"--filter", "label=com.docker.compose.service=db").Output()
	if err != nil || strings.TrimSpace(string(ids)) == "" {
		t.Fatalf("find db container: %v %q", err, ids)
	}
	id := strings.TrimSpace(string(ids))
	t.Cleanup(func() { restartDB(t, id) })
	if out, err := exec.Command("docker", "stop", id).CombinedOutput(); err != nil {
		t.Fatalf("stop db: %v %s", err, out)
	}
	code, body := do(t, http.MethodGet, url+"/v1/health")
	if code != http.StatusServiceUnavailable || body != "{\"status\":\"unavailable\"}\n" {
		t.Fatalf("got %d %q", code, body)
	}
}

// restartDB starts the stopped container and waits until its health check passes.
func restartDB(t *testing.T, id string) {
	t.Helper()
	if out, err := exec.Command("docker", "start", id).CombinedOutput(); err != nil {
		t.Errorf("start db: %v %s", err, out)
		return
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		out, _ := exec.Command("docker", "inspect", "-f", "{{.State.Health.Status}}", id).Output()
		if strings.TrimSpace(string(out)) == "healthy" {
			return
		}
		time.Sleep(time.Second)
	}
	t.Errorf("db not healthy 60s after restart")
}
