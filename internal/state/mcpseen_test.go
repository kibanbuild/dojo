package state

import (
	"os"
	"runtime"
	"testing"
	"time"
)

func TestMCPSeenRoundTrip(t *testing.T) {
	t.Setenv("DOJO_HOME", t.TempDir())
	if _, ok, err := LoadMCPSeen(); ok || err != nil {
		t.Fatalf("missing file = ok %v, err %v", ok, err)
	}
	at := time.Date(2026, 11, 20, 9, 30, 0, 0, time.UTC)
	if err := SaveMCPSeen(MCPSeen{Client: "claude-code", At: at}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := LoadMCPSeen()
	if err != nil || !ok || got.Client != "claude-code" || !got.At.Equal(at) {
		t.Fatalf("got %+v ok %v err %v", got, ok, err)
	}
	dir, _ := Dir()
	fi, err := os.Stat(dir + "/mcp-seen.json")
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", fi.Mode().Perm())
	}
}

func TestMCPSeenDamagedFileIsAnError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DOJO_HOME", dir)
	if err := os.WriteFile(dir+"/mcp-seen.json", []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadMCPSeen(); err == nil {
		t.Fatal("want an error")
	}
}
