package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"
)

// ProgressEvent is one queued progress event, in the shape POST /v1/events takes. Optional fields
// are omitted when unset; the pointers keep a real zero (duration_ms 0, passed 0) on the wire.
type ProgressEvent struct {
	ID             string `json:"id"`
	Type           string `json:"type"`
	At             string `json:"at"`
	ItemID         string `json:"item_id,omitempty"` // empty only for doctor.run
	Lab            string `json:"lab,omitempty"`
	CatalogVersion string `json:"catalog_version,omitempty"`
	Reason         string `json:"reason,omitempty"`
	Detail         string `json:"detail,omitempty"`
	Result         string `json:"result,omitempty"`
	DurationMS     *int   `json:"duration_ms,omitempty"`
	Passed         *int   `json:"passed,omitempty"`
	Total          *int   `json:"total,omitempty"`
	Output         string `json:"output,omitempty"`
	Commit         string `json:"commit,omitempty"` // ticket.commit: 40 lowercase hex
	// Checks is the report of a doctor.run event.
	Checks []DoctorCheck `json:"checks,omitempty"`
}

// DoctorCheck is one check of a queued doctor.run event.
type DoctorCheck struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Text   string `json:"text"`
	Fix    string `json:"fix"`
}

// EventQueue is the progress events waiting to be sent. Account is the learner they belong to, when
// known. Hold keeps sending off after a 401 until the next login; RetryAfter holds it back after a
// 429 or an outage.
type EventQueue struct {
	Account    string          `json:"account,omitempty"`
	Hold       bool            `json:"hold_until_login,omitempty"`
	RetryAfter time.Time       `json:"retry_after,omitzero"`
	Events     []ProgressEvent `json:"events"`
}

// MaxEvents bounds the events queue file: the oldest are dropped past it.
const MaxEvents = 500

const (
	eventsFile  = "events-queue.json"
	accountFile = "account"
)

// LoadEvents reads the events queue; a missing file is an empty queue.
func LoadEvents() (EventQueue, error) {
	var q EventQueue
	p, err := telemetryPath(eventsFile)
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
		return EventQueue{}, fmt.Errorf("%w: %w", ErrCorruptEvents, err)
	}
	return q, nil
}

// ErrCorruptEvents means the queue file exists but is not valid JSON; other read errors are not it.
var ErrCorruptEvents = errors.New("events queue file is corrupt")

// SaveEvents writes the queue atomically, keeping only the newest MaxEvents. An empty queue with no
// hold, backoff or account removes the file.
func SaveEvents(q EventQueue) error {
	if len(q.Events) > MaxEvents {
		q.Events = q.Events[len(q.Events)-MaxEvents:]
	}
	if len(q.Events) == 0 {
		q.Hold, q.RetryAfter = false, time.Time{}
		return RemoveEvents()
	}
	return writeJSON(eventsFile, q)
}

// RemoveEvents drops every waiting event; a missing file is not an error.
func RemoveEvents() error {
	return removeInHome(eventsFile)
}

// LoadAccount returns the id of the account this computer last logged in as, or "" when unknown.
func LoadAccount() string {
	p, err := telemetryPath(accountFile)
	if err != nil {
		return ""
	}
	raw, err := os.ReadFile(p) //nolint:gosec // G304: p is the dojo home from the user's own environment
	if err != nil {
		return ""
	}
	var id string
	if json.Unmarshal(raw, &id) != nil {
		return ""
	}
	return strings.TrimSpace(id)
}

// SaveAccount remembers the logged-in account's id, so events queued later carry their owner.
func SaveAccount(id string) error {
	return writeJSON(accountFile, id)
}

// RemoveAccount forgets the account id (logout).
func RemoveAccount() error {
	return removeInHome(accountFile)
}

func removeInHome(name string) error {
	p, err := telemetryPath(name)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
