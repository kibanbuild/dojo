package mentor

import (
	"strings"
	"testing"
)

func TestAgentsMDCarriesTheRules(t *testing.T) {
	got := AgentsMD()
	for _, want := range []string{"Never write the fix", "man pages", "Ask what they tried"} {
		if !strings.Contains(got, want) {
			t.Errorf("AgentsMD missing %q", want)
		}
	}
	if !strings.HasSuffix(got, "\n") || strings.HasSuffix(got, "\n\n") {
		t.Errorf("AgentsMD should end with exactly one newline: %q", got[len(got)-3:])
	}
}

func TestClaudeMDImportsAgentsMD(t *testing.T) {
	if ClaudeMD != "@AGENTS.md\n" {
		t.Errorf("ClaudeMD = %q", ClaudeMD)
	}
}
