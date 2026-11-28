package doctor

import (
	"strings"
	"testing"
	"time"
)

func TestMCPCheck(t *testing.T) {
	now := time.Date(2026, 12, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		client string
		at     time.Time
		seen   bool
		status Status
		text   string
		fix    bool
	}{
		{"never", "", time.Time{}, false, Warn, "No AI has connected to dojo mcp yet", true},
		{"recent", "claude-code", now.Add(-48 * time.Hour), true, OK, "Your AI connected: claude-code, " + now.Add(-48*time.Hour).Local().Format("2006-01-02"), false},
		{"29 days", "cursor", now.Add(-29 * 24 * time.Hour), true, OK, "Your AI connected: cursor, ", false},
		{"30 days", "cursor", now.Add(-30 * 24 * time.Hour), true, Warn, "No AI has connected to dojo mcp yet", true},
		{"clock ahead", "vscode", now.Add(time.Hour), true, OK, "Your AI connected: vscode, ", false},
		{"empty client", "", now.Add(-time.Hour), true, OK, "Your AI connected: unknown, ", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := MCP(tt.client, tt.at, tt.seen, now)
			if r.ID != "mcp" || r.Group != GroupAdvice || r.Status != tt.status || !strings.HasPrefix(r.Text, tt.text) {
				t.Errorf("got %+v", r)
			}
			if (r.Fix != "") != tt.fix {
				t.Errorf("fix = %q", r.Fix)
			}
			if tt.fix && r.Fix != "Add the config from Settings > Your AI, restart your AI, then run dojo doctor again" {
				t.Errorf("fix = %q", r.Fix)
			}
		})
	}
}

func TestMCPNeverBlocks(t *testing.T) {
	if !Ready([]Result{MCP("", time.Time{}, false, time.Now())}) {
		t.Fatal("a missing AI must not block")
	}
}
