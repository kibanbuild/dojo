package state

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"time"
)

// MCPSeen is DOJO_HOME/mcp-seen.json: the last AI client that opened dojo mcp.
type MCPSeen struct {
	Client string    `json:"client"`
	At     time.Time `json:"at"`
}

// LoadMCPSeen reads the marker; ok is false when the file does not exist.
func LoadMCPSeen() (m MCPSeen, ok bool, err error) {
	p, err := telemetryPath("mcp-seen.json")
	if err != nil {
		return m, false, err
	}
	raw, err := os.ReadFile(p) //nolint:gosec // G304: p is the dojo home from the user's own environment
	if errors.Is(err, fs.ErrNotExist) {
		return m, false, nil
	}
	if err != nil {
		return m, false, err
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return MCPSeen{}, false, err
	}
	return m, true, nil
}

// SaveMCPSeen writes the marker atomically with mode 0600.
func SaveMCPSeen(m MCPSeen) error {
	return writeJSON("mcp-seen.json", m)
}
