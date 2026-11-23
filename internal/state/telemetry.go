package state

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Telemetry is the local usage-stats switch. The zero value is the default: on, once the notice
// has been shown (NoticeShown), so nothing is sent before the learner has been told.
type Telemetry struct {
	Off         bool `json:"off,omitempty"`
	NoticeShown bool `json:"notice_shown,omitempty"`
}

// Event is one queued usage event: names and a timing only, never arguments, paths or output.
type Event struct {
	Command    string `json:"command"`
	Result     string `json:"result"`
	DurationMS int    `json:"duration_ms"`
}

// Queue is the events waiting to be sent. RetryAfter holds sending back after a 429 or an outage.
type Queue struct {
	RetryAfter time.Time `json:"retry_after,omitzero"`
	Events     []Event   `json:"events"`
}

// MaxQueued bounds the queue file: the oldest events are dropped past it.
const MaxQueued = 100

func telemetryPath(name string) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// LoadTelemetry reads the switch; a missing file is the default.
func LoadTelemetry() (Telemetry, error) {
	var t Telemetry
	p, err := telemetryPath("telemetry.json")
	if err != nil {
		return t, err
	}
	raw, err := os.ReadFile(p) //nolint:gosec // G304: p is the dojo home from the user's own environment
	if errors.Is(err, fs.ErrNotExist) {
		return t, nil
	}
	if err != nil {
		return t, err
	}
	if err := json.Unmarshal(raw, &t); err != nil {
		return Telemetry{}, err
	}
	return t, nil
}

// SaveTelemetry writes the switch atomically with mode 0600.
func SaveTelemetry(t Telemetry) error {
	return writeJSON("telemetry.json", t)
}

// LoadQueue reads the queue; a missing file is an empty queue.
func LoadQueue() (Queue, error) {
	var q Queue
	p, err := telemetryPath("telemetry-queue.json")
	if err != nil {
		return q, err
	}
	raw, err := os.ReadFile(p) //nolint:gosec // G304: p is the dojo home from the user's own environment
	if errors.Is(err, fs.ErrNotExist) {
		return q, nil
	}
	if err != nil {
		return q, err
	}
	if err := json.Unmarshal(raw, &q); err != nil {
		return Queue{}, err
	}
	return q, nil
}

// SaveQueue writes the queue atomically, keeping only the newest MaxQueued events. An empty queue
// with no backoff removes the file.
func SaveQueue(q Queue) error {
	if len(q.Events) > MaxQueued {
		q.Events = q.Events[len(q.Events)-MaxQueued:]
	}
	if len(q.Events) == 0 && q.RetryAfter.IsZero() {
		return RemoveQueue()
	}
	return writeJSON("telemetry-queue.json", q)
}

// RemoveQueue drops everything waiting to be sent; a missing file is not an error.
func RemoveQueue() error {
	p, err := telemetryPath("telemetry-queue.json")
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// writeJSON writes v to a file in the dojo home: a 0600 temp file synced and renamed over the old
// one in a 0700 directory, so a crash never leaves a half-written file.
func writeJSON(name string, v any) error {
	p, err := telemetryPath(name)
	if err != nil {
		return err
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".telemetry-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		return err
	}
	if _, err := f.Write(append(raw, '\n')); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		return err
	}
	ok = true
	return nil
}
