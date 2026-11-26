package server

import (
	"testing"
	"time"

	"github.com/kibanbuild/dojo/internal/server/repo"
)

func TestNextStep(t *testing.T) {
	both := repo.Readiness{O3: true, O4: true}
	for _, c := range []struct {
		name string
		in   onboardingInputs
		want string
	}{
		{"no path", onboardingInputs{}, "o0"},
		{"path, no CLI", onboardingInputs{chosen: true}, "o2"},
		{"CLI, nothing ready", onboardingInputs{chosen: true, cliConnected: true}, "o3"},
		{"O4 ready alone does not skip O3", onboardingInputs{chosen: true, cliConnected: true, ready: repo.Readiness{O4: true}}, "o3"},
		{"machine ready", onboardingInputs{chosen: true, cliConnected: true, ready: repo.Readiness{O3: true}}, "o4"},
		{"everything ready", onboardingInputs{chosen: true, cliConnected: true, ready: both}, "o5"},
		{"hosted default skips o3", onboardingInputs{chosen: true, cliConnected: true, hostedDefault: true}, "o4"},
		{"hosted default with O4 ready", onboardingInputs{chosen: true, cliConnected: true, hostedDefault: true, ready: repo.Readiness{O4: true}}, "o5"},
		{"hosted default does not skip o2", onboardingInputs{chosen: true, hostedDefault: true}, "o2"},
		{"hosted default does not skip o0", onboardingInputs{hostedDefault: true}, "o0"},
		{"hosted default does not undo done", onboardingInputs{finished: true, hostedDefault: true}, "done"},
		{"o0 comes before o2", onboardingInputs{ready: both}, "o0"},
		{"o2 comes before o3", onboardingInputs{chosen: true}, "o2"},
		{"done is sticky: nothing else set", onboardingInputs{finished: true}, "done"},
		{"done is sticky: CLI revoked, checks gone", onboardingInputs{finished: true, chosen: true}, "done"},
		{"done with everything ready", onboardingInputs{finished: true, chosen: true, cliConnected: true, ready: both}, "done"},
	} {
		if got := nextStep(c.in); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestLoginPayloadIsAStreamLoginFrame(t *testing.T) {
	at := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	name, data := streamFrame(repo.LoginPayload("ada's laptop", "linux/amd64", "v1.2.3", at))
	want := `{"at":"2026-11-26T09:00:00Z","cli_version":"v1.2.3","device_name":"ada's laptop","os":"linux/amd64"}`
	if name != "login" || string(data) != want {
		t.Fatalf("frame: %q %s", name, data)
	}
}
