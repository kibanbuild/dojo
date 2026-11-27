//go:build integration

package server_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/kibanyadev/dojo/internal/server"
)

// telemetryBody is one event batch; marker is a unique cli_version so a test counts only its rows.
func telemetryBody(marker, command string) map[string]any {
	return map[string]any{"cli_version": marker, "os": "linux/amd64", "events": []map[string]any{
		{"command": command, "result": "ok", "duration_ms": 42},
	}}
}

func telemetryMarker(t *testing.T) string {
	t.Helper()
	marker := fmt.Sprintf("it-%d", browserSeq.Add(1)+1000)
	cleanup := "DELETE FROM telemetry_events WHERE cli_version = '" + marker + "'"
	asOwner(t, cleanup)
	t.Cleanup(func() { asOwner(t, cleanup) })
	return marker
}

func eventCount(t *testing.T, marker string) string {
	t.Helper()
	return asOwner(t, "SELECT count(*) FROM telemetry_events WHERE cli_version = '"+marker+"'")
}

func TestTelemetryAnonymousInsertHasNoLearner(t *testing.T) {
	marker := telemetryMarker(t)
	c := newCLIClient(t)
	if code, body := c.call(http.MethodPost, "/v1/telemetry", "", telemetryBody(marker, "start")); code != http.StatusNoContent || body != "" {
		t.Fatalf("anonymous: %d %q", code, body)
	}
	row := asOwner(t, "SELECT learner_id IS NULL, command, result, duration_ms, os, org_id = (SELECT id FROM orgs WHERE slug = 'public'), at > now() - interval '1 minute' FROM telemetry_events WHERE cli_version = '"+marker+"'")
	if row != "t|start|ok|42|linux/amd64|t|t" {
		t.Fatalf("stored row: %q", row)
	}
}

func TestTelemetryBearerLinksTheLearner(t *testing.T) {
	resetLearners(t, "1001")
	signUp(t, "ada")
	marker := telemetryMarker(t)
	c := newCLIClient(t)
	tok := cliToken(t, c, "ada")
	if code, body := c.call(http.MethodPost, "/v1/telemetry", tok, telemetryBody(marker, "check")); code != http.StatusNoContent || body != "" {
		t.Fatalf("bearer: %d %q", code, body)
	}
	linked := asOwner(t, "SELECT count(*) FROM telemetry_events e JOIN learners l ON l.id = e.learner_id WHERE l.github_id = 1001 AND e.org_id = l.org_id AND e.cli_version = '"+marker+"'")
	if linked != "1" {
		t.Fatalf("linked rows: %q, want 1", linked)
	}

	// A bad token is refused, never recorded as anonymous.
	if code, _ := c.call(http.MethodPost, "/v1/telemetry", "dojo_"+strings.Repeat("x", 43), telemetryBody(marker, "check")); code != http.StatusUnauthorized {
		t.Fatalf("bad token: %d", code)
	}
	if n := eventCount(t, marker); n != "1" {
		t.Fatalf("rows after a refused call: %q", n)
	}

	// Rows go with the learner (the cascade delete_learner() relies on).
	asOwner(t, "DELETE FROM learners WHERE github_id = 1001")
	if n := eventCount(t, marker); n != "0" {
		t.Fatalf("rows after deleting the learner: %q, want 0", n)
	}
}

func TestTelemetryUsageStatsOffStoresNothing(t *testing.T) {
	resetLearners(t, "1001")
	signUp(t, "ada")
	marker := telemetryMarker(t)
	c := newCLIClient(t)
	tok := cliToken(t, c, "ada")
	asOwner(t, "UPDATE learners SET usage_stats = false WHERE github_id = 1001")
	if code, body := c.call(http.MethodPost, "/v1/telemetry", tok, telemetryBody(marker, "check")); code != http.StatusNoContent || body != "" {
		t.Fatalf("usage_stats off: %d %q", code, body)
	}
	if n := eventCount(t, marker); n != "0" {
		t.Fatalf("stored %q rows for a learner with usage_stats off", n)
	}
	asOwner(t, "UPDATE learners SET usage_stats = true WHERE github_id = 1001")
	if code, _ := c.call(http.MethodPost, "/v1/telemetry", tok, telemetryBody(marker, "check")); code != http.StatusNoContent || eventCount(t, marker) != "1" {
		t.Fatalf("usage_stats back on: %d, rows %q", code, eventCount(t, marker))
	}
}

func TestTelemetryCookieNeedsCSRF(t *testing.T) {
	resetLearners(t, "1001")
	marker := telemetryMarker(t)
	b := newBrowser(t)
	if resp := b.signIn("ada", "", "age=on&terms=on", false); resp.Header.Get("Location") != "/app" {
		t.Fatalf("sign-in: %q", resp.Header.Get("Location"))
	}
	post := func(csrf bool) int {
		req, _ := http.NewRequest(http.MethodPost, apiURL(t)+"/v1/telemetry", strings.NewReader(
			`{"cli_version":"`+marker+`","os":"linux","events":[{"command":"check","result":"ok","duration_ms":1}]}`))
		req.Header.Set("Content-Type", "application/json")
		if csrf {
			req.Header.Set(server.CSRFHeader, "1")
		}
		return b.do(req).StatusCode
	}
	if code := post(false); code != http.StatusForbidden || eventCount(t, marker) != "0" {
		t.Fatalf("cookie without CSRF: %d, rows %q", code, eventCount(t, marker))
	}
	if code := post(true); code != http.StatusNoContent || eventCount(t, marker) != "1" {
		t.Fatalf("cookie with CSRF: %d, rows %q", code, eventCount(t, marker))
	}
}

func TestTelemetryAnonymousIsRateLimitedPerIP(t *testing.T) {
	marker := telemetryMarker(t)
	c := newCLIClient(t)
	var limited int
	for i := 0; i < 70; i++ {
		code, _ := c.call(http.MethodPost, "/v1/telemetry", "", telemetryBody(marker, "check"))
		if code == http.StatusTooManyRequests {
			limited++
		} else if code != http.StatusNoContent {
			t.Fatalf("call %d: %d", i, code)
		}
	}
	if limited == 0 || limited > 10 {
		t.Fatalf("%d of 70 calls limited, want a few past the burst of 60", limited)
	}
}

func TestTelemetryAuthenticatedIsRateLimitedPerLearner(t *testing.T) {
	resetLearners(t, "1001")
	signUp(t, "ada")
	marker := telemetryMarker(t)
	tok := cliToken(t, newCLIClient(t), "ada")
	// Each call comes from a fresh client IP: only a per-learner budget can stop them.
	var limited int
	for i := 0; i < 130; i++ {
		code, _ := newCLIClient(t).call(http.MethodPost, "/v1/telemetry", tok, telemetryBody(marker, "check"))
		if code == http.StatusTooManyRequests {
			limited++
		} else if code != http.StatusNoContent {
			t.Fatalf("call %d: %d", i, code)
		}
	}
	if limited == 0 || limited > 15 {
		t.Fatalf("%d of 130 calls limited, want a few past the burst of 120", limited)
	}
}
