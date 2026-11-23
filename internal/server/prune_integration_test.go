//go:build integration

package server_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// runPruneJob runs the compose `prune` service, the same path as `make prune`, in the test project.
func runPruneJob(t *testing.T) string {
	t.Helper()
	project := os.Getenv("DOJO_COMPOSE_PROJECT")
	cmd := exec.Command("docker", "compose", "-p", project, "-f", "compose.yaml", "-f", "compose.it.yaml",
		"--env-file", ".env.example", "--profile", "jobs", "run", "--rm", "--no-deps", "-T", "prune")
	cmd.Dir = "../.."
	cmd.Env = append(os.Environ(), "EDGE_PORT=", "FAKE_GITHUB_PORT=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("prune job: %v\n%s", err, out)
	}
	return string(out)
}

func seedTelemetryAt(t *testing.T, marker, age string) {
	t.Helper()
	asOwner(t, "INSERT INTO telemetry_events (org_id, command, result, duration_ms, cli_version, os, at) "+
		"SELECT id, 'check', 'ok', 1, '"+marker+"', 'linux', now() - interval '"+age+"' FROM orgs WHERE slug = 'public'")
}

func TestPruneTelemetryKeepsTwelveMonths(t *testing.T) {
	marker := telemetryMarker(t)
	// Only this test's old rows may exist, so the job's count is exact.
	asOwner(t, "DELETE FROM telemetry_events WHERE at < now() - interval '12 months'")
	for _, age := range []string{"13 months", "12 months 1 day", "400 days", "11 months 29 days", "1 hour"} {
		seedTelemetryAt(t, marker, age)
	}
	out := runPruneJob(t)
	if !strings.Contains(out, "prune: telemetry_events deleted 3") {
		t.Fatalf("job output: %q, want 3 deleted", out)
	}
	if got := eventCount(t, marker); got != "2" {
		t.Fatalf("rows left: %q, want the 2 newer than 12 months", got)
	}
	if got := asOwner(t, "SELECT count(*) FROM telemetry_events WHERE cli_version = '"+marker+"' AND at < now() - interval '12 months'"); got != "0" {
		t.Fatalf("old rows left: %q", got)
	}

	// Idempotent: a second run finds nothing.
	if out := runPruneJob(t); !strings.Contains(out, "prune: telemetry_events deleted 0") {
		t.Fatalf("second run: %q, want 0 deleted", out)
	}
	if got := eventCount(t, marker); got != "2" {
		t.Fatalf("rows after second run: %q, want 2", got)
	}
}

func TestPruneTelemetryFunctionReturnsCount(t *testing.T) {
	marker := telemetryMarker(t)
	asOwner(t, "DELETE FROM telemetry_events WHERE at < now() - interval '12 months'")
	seedTelemetryAt(t, marker, "2 years")
	seedTelemetryAt(t, marker, "3 years")
	if got := asOwner(t, "SELECT prune_telemetry()"); got != "2" {
		t.Fatalf("prune_telemetry() = %q, want 2", got)
	}
	if got := asOwner(t, "SELECT prune_telemetry()"); got != "0" {
		t.Fatalf("second call = %q, want 0", got)
	}
}

// The REVOKE itself: without it SECURITY INVOKER still refuses, so check the privilege directly.
func TestPruneTelemetryIsNotExecutableByServiceRoles(t *testing.T) {
	for _, role := range []string{"dojo_api", "dojo_admin", "public"} {
		got := asOwner(t, "SELECT has_function_privilege('"+role+"', 'prune_telemetry()', 'EXECUTE')")
		if got != "f" {
			t.Fatalf("%s can execute prune_telemetry(): %q", role, got)
		}
	}
}
