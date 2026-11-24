package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kibanbuild/dojo/internal/state"
)

func TestStatusWithNothingRunningSaysSo(t *testing.T) {
	e := newEnv(t)
	e.lab.noLab = true
	if err := e.run(t, "status"); err != nil {
		t.Fatalf("status: %v", err)
	}
	for _, want := range []string{"No ticket in progress", "Lab: none", "Login: not signed in", "Events: none waiting"} {
		if !strings.Contains(e.out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, e.out)
		}
	}
}

func TestStatusShowsTicketLabLoginAndQueue(t *testing.T) {
	e := newEnv(t)
	if err := e.run(t, "start", "white/a"); err != nil {
		t.Fatal(err)
	}
	if err := state.SaveToken("tok"); err != nil {
		t.Fatal(err)
	}
	q := state.EventQueue{Events: []state.ProgressEvent{{ID: "1"}, {ID: "2"}}}
	if err := state.SaveEvents(q); err != nil {
		t.Fatal(err)
	}
	if err := e.run(t, "status"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"white/a: Ticket A", "Fix A.", "Lab: running (fake)", "Login: signed in", "Events: 2 waiting"} {
		if !strings.Contains(e.out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, e.out)
		}
	}
	if strings.Contains(e.out.String(), "held") {
		t.Errorf("sending is not held:\n%s", e.out)
	}
}

func TestStatusSaysWhenSendingIsHeld(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		q    state.EventQueue
		want string
	}{
		{"until login", state.EventQueue{Hold: true, Events: []state.ProgressEvent{{ID: "1"}}},
			"Events: 1 waiting, sending held until the next dojo login"},
		{"until time", state.EventQueue{RetryAfter: now.Add(5 * time.Minute), Events: []state.ProgressEvent{{ID: "1"}}},
			"Events: 1 waiting, sending held until 12:05 UTC"},
		// local time is UTC in this case (see the next test for a non-UTC zone)
		{"backoff over", state.EventQueue{RetryAfter: now.Add(-time.Minute), Events: []state.ProgressEvent{{ID: "1"}}},
			"Events: 1 waiting\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			old := time.Local
			time.Local = time.UTC
			t.Cleanup(func() { time.Local = old })
			e := newEnv(t)
			if err := state.SaveEvents(tt.q); err != nil {
				t.Fatal(err)
			}
			if err := e.run(t, "status"); err != nil || !strings.Contains(e.out.String(), tt.want) {
				t.Errorf("err=%v want %q in:\n%s", err, tt.want, e.out)
			}
		})
	}
}

func TestStatusShowsHeldTimeInLocalTime(t *testing.T) {
	old := time.Local
	time.Local = time.FixedZone("TST", 2*3600)
	t.Cleanup(func() { time.Local = old })
	e := newEnv(t)
	q := state.EventQueue{RetryAfter: time.Date(2026, 11, 16, 12, 5, 0, 0, time.UTC), Events: []state.ProgressEvent{{ID: "1"}}}
	if err := state.SaveEvents(q); err != nil {
		t.Fatal(err)
	}
	if err := e.run(t, "status"); err != nil || !strings.Contains(e.out.String(), "held until 14:05 TST") {
		t.Errorf("err=%v\n%s", err, e.out)
	}
}

func TestStatusSurvivesCorruptFiles(t *testing.T) {
	e := newEnv(t)
	home := os.Getenv("DOJO_HOME")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"state.json", "events-queue.json"} {
		if err := os.WriteFile(filepath.Join(home, f), []byte("{nope"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.run(t, "status"); err != nil {
		t.Fatalf("status: %v", err)
	}
	for _, want := range []string{"Ticket: unreadable", "state.json", "Lab:", "Login:", "Events: unreadable"} {
		if !strings.Contains(e.out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, e.out)
		}
	}
}

func TestStatusNeverCallsTheNetwork(t *testing.T) {
	e, api := eventsEnv(t, true)
	if err := e.run(t, "start", "white/a"); err != nil {
		t.Fatal(err)
	}
	before := len(api.sent())
	if err := state.SaveEvents(state.EventQueue{Events: []state.ProgressEvent{{ID: "x", Type: "check.run", ItemID: "white/a"}}}); err != nil {
		t.Fatal(err)
	}
	e.app.HTTP = nil
	if err := e.app.status(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(api.sent()) != before {
		t.Error("status sent to the API")
	}
}

func TestStatusSurvivesUnavailableBackend(t *testing.T) {
	e := newEnv(t)
	e.run(t, "start", "white/a")
	e.lab.notReady = errors.New("no limactl")
	if err := e.run(t, "status"); err != nil || !strings.Contains(e.out.String(), "Lab: unknown") {
		t.Errorf("err=%v\n%s", err, e.out)
	}
}

func TestHintPrintsTheTicketPageWhenAnAPIIsConfigured(t *testing.T) {
	e := newEnv(t)
	e.app.Login.APIURL = "https://dojo.example/"
	if err := e.run(t, "hint"); err == nil || !strings.Contains(err.Error(), "no ticket in progress") {
		t.Errorf("no ticket: %v", err)
	}
	e.run(t, "start", "white/a")
	if err := e.run(t, "hint"); err != nil || !strings.Contains(e.out.String(), "https://dojo.example/app/t/white/a") {
		t.Errorf("hint: %v\n%s", err, e.out)
	}
	st, _ := state.Load()
	st.Current = "white/a b?"
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	if err := e.run(t, "hint"); err != nil || !strings.Contains(e.out.String(), "https://dojo.example/app/t/white/a%20b%3F\n") {
		t.Errorf("escaped hint: %v\n%s", err, e.out)
	}
	st.Current = "white/a"
	_ = st.Save()
	if st, _ := state.Load(); st.HintsShown != 0 {
		t.Errorf("the URL is not a shown hint: %d", st.HintsShown)
	}
}
