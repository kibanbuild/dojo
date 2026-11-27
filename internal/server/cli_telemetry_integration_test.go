//go:build integration

package server_test

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kibanyadev/dojo/internal/cli"
	"github.com/kibanyadev/dojo/internal/state"
)

// telemetryHome points DOJO_HOME at a fresh directory and tags the CLI build with a unique version,
// so a test counts only its own rows.
func telemetryHome(t *testing.T) string {
	t.Helper()
	t.Setenv("DOJO_HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("DOJO_TELEMETRY", "")
	t.Setenv("DO_NOT_TRACK", "")
	marker := telemetryMarker(t)
	old := cli.Version
	cli.Version = marker
	t.Cleanup(func() { cli.Version = old })
	return marker
}

// meUsageStats reads usage_stats from /v1/me with the stored CLI token.
func meUsageStats(t *testing.T) string {
	t.Helper()
	tok, err := state.LoadToken()
	if err != nil {
		t.Fatal(err)
	}
	code, body := newCLIClient(t).call(http.MethodGet, "/v1/me", tok, nil)
	if code != http.StatusOK {
		t.Fatalf("me: %d %q", code, body)
	}
	switch {
	case strings.Contains(body, `"usage_stats":true`):
		return "true"
	case strings.Contains(body, `"usage_stats":false`):
		return "false"
	}
	t.Fatalf("me without usage_stats: %q", body)
	return ""
}

func TestCLITelemetryLoggedInRowIsLinked(t *testing.T) {
	resetLearners(t, "1001")
	signUp(t, "ada")
	marker := telemetryHome(t)
	if out, err := runCLI(t, "ada", "login"); err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}
	if n := eventCount(t, marker); n != "0" {
		t.Fatalf("rows before the notice was shown on a prior command: %s", n)
	}
	_, _ = runCLI(t, "", "hint") // no ticket: result error, still an event
	linked := asOwner(t, "SELECT count(*) FROM telemetry_events e JOIN learners l ON l.id = e.learner_id WHERE l.github_id = 1001 AND e.cli_version = '"+marker+"' AND e.command = 'hint' AND e.result = 'error'")
	if linked != "1" {
		t.Fatalf("linked rows: %q, want 1", linked)
	}
}

func TestCLITelemetryAnonymousRowHasNoLearner(t *testing.T) {
	marker := telemetryHome(t)
	out, _ := runCLI(t, "", "status")
	if !strings.Contains(out, "usage stats") {
		t.Fatalf("no notice: %q", out)
	}
	if n := eventCount(t, marker); n != "0" {
		t.Fatalf("rows from the notice command: %s", n)
	}
	_, _ = runCLI(t, "", "status")
	row := asOwner(t, "SELECT count(*), count(*) FILTER (WHERE learner_id IS NULL) FROM telemetry_events WHERE cli_version = '"+marker+"'")
	if row != "1|1" {
		t.Fatalf("anonymous rows: %q, want 1|1", row)
	}
}

func TestCLITelemetryOffMirrorsAndStoresNothing(t *testing.T) {
	resetLearners(t, "1001")
	signUp(t, "ada")
	marker := telemetryHome(t)
	if out, err := runCLI(t, "ada", "login"); err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}
	if got := meUsageStats(t); got != "true" {
		t.Fatalf("usage_stats before: %s", got)
	}
	if out, err := runCLI(t, "", "telemetry", "off"); err != nil || !strings.Contains(out, "account setting is off too") {
		t.Fatalf("off: %v\n%s", err, out)
	}
	if got := meUsageStats(t); got != "false" {
		t.Fatalf("usage_stats after off: %s", got)
	}
	for range 2 {
		_, _ = runCLI(t, "", "status")
	}
	if n := eventCount(t, marker); n != "0" {
		t.Fatalf("rows stored while off: %s", n)
	}

	if out, err := runCLI(t, "", "telemetry", "on"); err != nil || !strings.Contains(out, "account setting is on too") {
		t.Fatalf("on: %v\n%s", err, out)
	}
	if got := meUsageStats(t); got != "true" {
		t.Fatalf("usage_stats after on: %s", got)
	}
	_, _ = runCLI(t, "", "status")
	if n := eventCount(t, marker); n != "1" {
		t.Fatalf("rows after on: %s, want 1", n)
	}
}

func TestCLILoginAlignsSwitchFromAccount(t *testing.T) {
	resetLearners(t, "1001")
	signUp(t, "ada")
	marker := telemetryHome(t)
	c := newCLIClient(t)
	tok := cliToken(t, c, "ada")
	if code, body := c.call(http.MethodPatch, "/v1/me", tok, map[string]bool{"usage_stats": false}); code != http.StatusOK {
		t.Fatalf("patch: %d %q", code, body)
	}
	out, err := runCLI(t, "ada", "login")
	if err != nil || !strings.Contains(out, "off on your account") {
		t.Fatalf("login: %v\n%s", err, out)
	}
	if st, _ := state.LoadTelemetry(); !st.Off {
		t.Fatal("local switch still on after the account said off")
	}
	for range 2 {
		_, _ = runCLI(t, "", "status")
	}
	if n := eventCount(t, marker); n != "0" {
		t.Fatalf("rows stored after login aligned to off: %s", n)
	}
}
