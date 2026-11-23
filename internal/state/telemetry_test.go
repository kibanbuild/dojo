package state

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestTelemetryDefaultsAndRoundTrip(t *testing.T) {
	t.Setenv("DOJO_HOME", filepath.Join(t.TempDir(), "home"))
	got, err := LoadTelemetry()
	if err != nil || got != (Telemetry{}) {
		t.Fatalf("default: %+v %v", got, err)
	}
	if err := SaveTelemetry(Telemetry{Off: true, NoticeShown: true}); err != nil {
		t.Fatal(err)
	}
	got, err = LoadTelemetry()
	if err != nil || !got.Off || !got.NoticeShown {
		t.Fatalf("round trip: %+v %v", got, err)
	}
}

func TestTelemetryCorruptFileIsAnError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("DOJO_HOME", home)
	_ = os.WriteFile(filepath.Join(home, "telemetry.json"), []byte("{nope"), 0o600)
	if _, err := LoadTelemetry(); err == nil {
		t.Fatal("a corrupt switch file must be an error, so callers can stay silent")
	}
}

func TestQueueBoundedAtomicAndPrivate(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("DOJO_HOME", home)
	var q Queue
	for i := range MaxQueued + 30 {
		q.Events = append(q.Events, Event{Command: "list", Result: "ok", DurationMS: i})
	}
	if err := SaveQueue(q); err != nil {
		t.Fatal(err)
	}
	got, err := LoadQueue()
	if err != nil || len(got.Events) != MaxQueued || got.Events[0].DurationMS != 30 {
		t.Fatalf("bounded queue: len %d first %+v err %v", len(got.Events), got.Events[0], err)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(home, "telemetry-queue.json"))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("queue mode: %v %v", fi, err)
		}
		di, _ := os.Stat(home)
		if di.Mode().Perm() != 0o700 {
			t.Fatalf("home mode %v", di.Mode())
		}
	}
	entries, _ := os.ReadDir(home)
	if len(entries) != 1 {
		t.Fatalf("a temp file was left behind: %v", entries)
	}
}

func TestQueueEmptyRemovesFileUnlessBackoff(t *testing.T) {
	home := t.TempDir()
	t.Setenv("DOJO_HOME", home)
	if err := SaveQueue(Queue{Events: []Event{{Command: "list", Result: "ok"}}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveQueue(Queue{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "telemetry-queue.json")); !os.IsNotExist(err) {
		t.Fatalf("an empty queue left a file: %v", err)
	}
	until := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	if err := SaveQueue(Queue{RetryAfter: until}); err != nil {
		t.Fatal(err)
	}
	got, _ := LoadQueue()
	if !got.RetryAfter.Equal(until) {
		t.Fatalf("backoff lost: %+v", got)
	}
	if err := RemoveQueue(); err != nil || RemoveQueue() != nil {
		t.Fatal("RemoveQueue must be idempotent")
	}
}
