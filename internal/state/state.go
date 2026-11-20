// Package state keeps the learner's local progress between commands.
package state

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"
)

// State is what the CLI remembers on this machine.
type State struct {
	Current    string               `json:"current,omitempty"`
	HintsShown int                  `json:"hints_shown,omitempty"`
	Passed     map[string]time.Time `json:"passed,omitempty"`
	// LastCheck is the tail of the last dojo check's output (at most MaxCheckOutput
	// bytes), for the mentor to read. Empty until a check has run in this lab.
	LastCheck string `json:"last_check,omitempty"`
	// SSHKey is the private key ssh dojo was last set up with; empty means auto-detect.
	SSHKey string `json:"ssh_key,omitempty"`
}

// MaxCheckOutput bounds LastCheck so the state file stays small.
const MaxCheckOutput = 16 << 10

// SetLastCheck keeps the last MaxCheckOutput bytes of out, starting on a character boundary.
func (s *State) SetLastCheck(out []byte) {
	if len(out) > MaxCheckOutput {
		out = out[len(out)-MaxCheckOutput:]
		for len(out) > 0 && !utf8.RuneStart(out[0]) {
			out = out[1:]
		}
	}
	s.LastCheck = string(out)
}

// Dir returns the dojo home directory, honouring DOJO_HOME.
func Dir() (string, error) {
	if dir := os.Getenv("DOJO_HOME"); dir != "" {
		return dir, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "dojo"), nil
}

// Path returns where the state file lives.
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "state.json"), nil
}

// Load reads the state, returning an empty one if none was saved yet.
func Load() (*State, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	s := &State{Passed: map[string]time.Time{}}
	raw, err := os.ReadFile(p) //nolint:gosec // G304: p is the dojo home from the user's own environment
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, s); err != nil {
		return nil, err
	}
	if s.Passed == nil {
		s.Passed = map[string]time.Time{}
	}
	return s, nil
}

// Save writes the state atomically.
func (s *State) Save() error {
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
