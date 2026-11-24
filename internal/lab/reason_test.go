package lab

import (
	"errors"
	"fmt"
	"testing"
)

func TestFailReason(t *testing.T) {
	base := errors.New("boom")
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"untagged", base, ""},
		{"disk", withReason(base, classify("write: No space left on device")), ReasonDisk},
		{"memory", withReason(base, classify("cannot allocate memory")), ReasonMemory},
		{"vm", withReason(base, ReasonVM), ReasonVM},
		{"first tag wins", withReason(withReason(base, ReasonDisk), ReasonVM), ReasonDisk},
		{"wrapped", fmt.Errorf("start: %w", withReason(base, ReasonVM)), ReasonVM},
		{"nil", nil, ""},
	}
	for _, tt := range tests {
		if got := FailReason(tt.err); got != tt.want {
			t.Errorf("%s: %q, want %q", tt.name, got, tt.want)
		}
	}
	if classify("something else") != "" {
		t.Error("unrelated stderr classified")
	}
	if !errors.Is(withReason(base, classify("x")), base) || FailReason(withReason(base, "")) != "" {
		t.Error("an empty reason must not wrap")
	}
}
