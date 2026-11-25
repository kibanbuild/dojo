package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kibanbuild/dojo/internal/doctor"
	"github.com/kibanbuild/dojo/internal/state"
)

func doctorEvents(evs []state.ProgressEvent) []state.ProgressEvent {
	var out []state.ProgressEvent
	for _, e := range evs {
		if e.Type == "doctor.run" {
			out = append(out, e)
		}
	}
	return out
}

func TestDoctorSendsDoctorRunWhenLoggedIn(t *testing.T) {
	e, api := eventsEnv(t, true)
	if err := e.run(t, "doctor"); err != nil {
		t.Fatalf("doctor: %v\n%s", err, e.out)
	}
	sent := api.sent()
	if len(sent) != 1 {
		t.Fatalf("requests = %d", len(sent))
	}
	if strings.Contains(sent[0], `"item_id"`) {
		t.Errorf("doctor.run carries no item_id: %s", sent[0])
	}
	evs := decodeEvents(t, sent[0])
	if len(evs) != 1 || evs[0].Type != "doctor.run" || !uuidRe.MatchString(evs[0].ID) {
		t.Fatalf("events = %+v", evs)
	}
	var memory *state.DoctorCheck
	for i, c := range evs[0].Checks {
		if c.ID == "memory" {
			memory = &evs[0].Checks[i]
		}
	}
	if memory == nil || memory.Status != "ok" || memory.Text != "16 GB RAM" {
		t.Errorf("memory check = %+v in %+v", memory, evs[0].Checks)
	}
	if q, _ := state.LoadEvents(); len(q.Events) != 0 {
		t.Errorf("queue after a sent run = %d", len(q.Events))
	}
}

func TestDoctorRunIsSentWhenNotReady(t *testing.T) {
	e, api := eventsEnv(t, true)
	e.app.Probe = func(context.Context) doctor.Machine { m := goodMachine; m.MemoryBytes = 4 << 30; return m }
	if err := e.run(t, "doctor"); !errors.Is(err, ErrNotReady) {
		t.Fatalf("doctor = %v", err)
	}
	if len(api.sent()) != 1 || !strings.Contains(api.sent()[0], `"status":"fail"`) {
		t.Errorf("sent = %v", api.sent())
	}
}

func TestDoctorLoggedOutQueuesOnlyTheLatestRun(t *testing.T) {
	e, api := eventsEnv(t, false)
	other := state.ProgressEvent{Type: "lab.stopped", ItemID: "white/a", ID: "keep", At: "2026-11-16T12:00:00Z"}
	if err := state.SaveEvents(state.EventQueue{Events: []state.ProgressEvent{other}}); err != nil {
		t.Fatal(err)
	}
	_ = e.run(t, "doctor")
	first, _ := state.LoadEvents()
	e.app.Probe = func(context.Context) doctor.Machine { m := goodMachine; m.MemoryBytes = 4 << 30; return m }
	_ = e.run(t, "doctor")
	q, _ := state.LoadEvents()
	runs := doctorEvents(q.Events)
	if len(runs) != 1 || len(q.Events) != 2 || q.Events[0].ID != "keep" {
		t.Fatalf("queue = %+v", q.Events)
	}
	if firstRuns := doctorEvents(first.Events); len(firstRuns) != 1 || firstRuns[0].ID == runs[0].ID {
		t.Errorf("the second run must replace the first: %+v then %+v", firstRuns, runs)
	}
	for _, c := range runs[0].Checks {
		if c.ID == "memory" && c.Status != "fail" {
			t.Errorf("the kept run is the latest one: memory = %+v", c)
		}
	}
	if len(api.sent()) != 0 {
		t.Errorf("logged out must not send: %v", api.sent())
	}
}

func TestDoctorRunQueuesWhenTheAPIIsDown(t *testing.T) {
	e, api := eventsEnv(t, true)
	api.mu.Lock()
	api.status = 500
	api.mu.Unlock()
	_ = e.run(t, "doctor")
	q, _ := state.LoadEvents()
	if len(doctorEvents(q.Events)) != 1 {
		t.Fatalf("queue = %+v", q.Events)
	}
}

func TestDoctorDevCatalogRecordsNothing(t *testing.T) {
	e, api := eventsEnv(t, true)
	e.app.Getenv = func(k string) string {
		if k == "DOJO_KATAS" {
			return "/some/dir"
		}
		return ""
	}
	_ = e.run(t, "doctor")
	if q, _ := state.LoadEvents(); len(q.Events) != 0 || len(api.sent()) != 0 {
		t.Error("DOJO_KATAS must send and queue nothing")
	}
}

func TestInterruptedDoctorRecordsNothing(t *testing.T) {
	e, _ := eventsEnv(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	e.app.Probe = func(context.Context) doctor.Machine { cancel(); return doctor.Machine{} }
	_ = e.app.Run(ctx, []string{"doctor"})
	if q, _ := state.LoadEvents(); len(q.Events) != 0 {
		t.Errorf("queue = %+v", q.Events)
	}
}
