package lab

import (
	"errors"
	"strings"
)

// Failure reasons a lab error can carry, as the events API names them.
const (
	ReasonDisk   = "disk"
	ReasonMemory = "memory"
	ReasonVM     = "vm"
)

type reasonError struct {
	reason string
	err    error
}

func (e *reasonError) Error() string { return e.err.Error() }
func (e *reasonError) Unwrap() error { return e.err }

// withReason tags err with a failure reason; an error that already has one keeps it.
func withReason(err error, reason string) error {
	if err == nil || reason == "" || FailReason(err) != "" {
		return err
	}
	return &reasonError{reason: reason, err: err}
}

// FailReason returns the reason a lab error was tagged with, or "" when it has none.
func FailReason(err error) string {
	var re *reasonError
	if errors.As(err, &re) {
		return re.reason
	}
	return ""
}

// classify reads a VM manager's stderr for a lack of disk or memory.
func classify(stderr string) string {
	s := strings.ToLower(stderr)
	switch {
	case strings.Contains(s, "no space left"), strings.Contains(s, "not enough disk"), strings.Contains(s, "disk full"):
		return ReasonDisk
	case strings.Contains(s, "cannot allocate memory"), strings.Contains(s, "not enough memory"),
		strings.Contains(s, "out of memory"), strings.Contains(s, "insufficient memory"):
		return ReasonMemory
	}
	return ""
}
