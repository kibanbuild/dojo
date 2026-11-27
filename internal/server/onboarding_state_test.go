package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kibanyadev/dojo/internal/server/repo"
)

func TestWaitlistOutIsNeverNull(t *testing.T) {
	for _, in := range [][]string{nil, {}} {
		b, err := json.Marshal(onboardingResponse{Waitlist: waitlistOut(in)})
		if err != nil || !strings.Contains(string(b), `"waitlist":[]`) {
			t.Fatalf("%v: %s %v", in, b, err)
		}
	}
	if got := waitlistOut([]string{"backend"}); len(got) != 1 || got[0] != "backend" {
		t.Fatalf("kept slugs: %v", got)
	}
}

func TestSelectFirstTicket(t *testing.T) {
	str := func(s string) *string { return &s }
	role := func(locked bool, items ...pathItem) pathRole { return pathRole{Locked: locked, Items: items} }
	a := func(state, commit *string) pathItem {
		return pathItem{ID: "r/a", Title: "A", State: state, CommitState: commit}
	}
	b := pathItem{ID: "r/b", Title: "B", State: str("started")}
	for _, c := range []struct {
		name      string
		roles     []pathRole
		id        string
		state, cs *string
	}{
		{"fresh learner", []pathRole{role(false, a(nil, nil), b)}, "r/a", nil, nil},
		{"started", []pathRole{role(false, a(str("started"), nil), b)}, "r/a", str("started"), nil},
		{"passed, pending", []pathRole{role(false, a(str("passed"), str("pending")), b)}, "r/a", str("passed"), str("pending")},
		{"passed, verified", []pathRole{role(false, a(str("passed"), str("verified")), b)}, "r/a", str("passed"), str("verified")},
		{"passed, unverified", []pathRole{role(false, a(str("passed"), str("unverified")), b)}, "r/a", str("passed"), str("unverified")},
		{"skips a locked role", []pathRole{role(true), role(false, a(nil, nil))}, "r/a", nil, nil},
	} {
		got := selectFirstTicket(c.roles)
		eq := func(x, y *string) bool { return (x == nil) == (y == nil) && (x == nil || *x == *y) }
		if got == nil || got.ID != c.id || !eq(got.State, c.state) || !eq(got.CommitState, c.cs) {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
	if got := selectFirstTicket([]pathRole{role(true)}); got != nil {
		t.Errorf("no tickets: %+v", got)
	}
	if got := selectFirstTicket(nil); got != nil {
		t.Errorf("no roles: %+v", got)
	}
}

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
