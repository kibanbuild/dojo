package state

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestLoadWithoutFileIsEmpty(t *testing.T) {
	t.Setenv("DOJO_HOME", t.TempDir())
	s, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if s.Current != "" || s.Passed == nil {
		t.Errorf("got %+v, want empty state with a usable Passed map", s)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	home := filepath.Join(t.TempDir(), "nested", "dojo")
	t.Setenv("DOJO_HOME", home)
	when := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	in := &State{Current: "white/a", HintsShown: 2, SSHKey: "/k/id_ed25519", Passed: map[string]time.Time{"white/a": when}}
	if err := in.Save(); err != nil {
		t.Fatal(err)
	}
	out, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if out.Current != in.Current || out.HintsShown != 2 || out.SSHKey != in.SSHKey || !out.Passed["white/a"].Equal(when) {
		t.Errorf("round trip: got %+v", out)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(home, "state.json"))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("state.json mode = %v, %v; want 0600", info.Mode().Perm(), err)
		}
	}
}

func TestLoadRejectsCorruptFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("DOJO_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "state.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Error("a corrupt state file should be an error, not silently reset progress")
	}
}

func TestSaveOverwritesExistingState(t *testing.T) {
	t.Setenv("DOJO_HOME", t.TempDir())
	for _, id := range []string{"white/a", "white/b"} {
		if err := (&State{Current: id}).Save(); err != nil {
			t.Fatalf("save %s: %v", id, err)
		}
	}
	got, err := Load()
	if err != nil || got.Current != "white/b" {
		t.Errorf("after two saves: %+v, %v", got, err)
	}
}

func TestDirDefaultsToUserConfigDir(t *testing.T) {
	t.Setenv("DOJO_HOME", "")
	dir, err := Dir()
	if err != nil {
		t.Skip("no user config dir on this machine:", err)
	}
	if filepath.Base(dir) != "dojo" || !filepath.IsAbs(dir) {
		t.Errorf("Dir() = %q, want an absolute path ending in dojo", dir)
	}
}

func TestSetLastCheckKeepsTheTailOnACharacterBoundary(t *testing.T) {
	var s State
	s.SetLastCheck([]byte("short"))
	if s.LastCheck != "short" {
		t.Errorf("LastCheck = %q", s.LastCheck)
	}
	// 2-byte runes: an odd cut lands inside one, which must be dropped, not kept as garbage.
	big := strings.Repeat("é", MaxCheckOutput) + "END"
	s.SetLastCheck([]byte(big))
	if len(s.LastCheck) > MaxCheckOutput || !strings.HasSuffix(s.LastCheck, "END") || !utf8.ValidString(s.LastCheck) {
		t.Errorf("len %d, valid %v, tail %q", len(s.LastCheck), utf8.ValidString(s.LastCheck), s.LastCheck[len(s.LastCheck)-5:])
	}
	if len(s.LastCheck) < MaxCheckOutput-4 {
		t.Errorf("kept only %d bytes, want nearly %d", len(s.LastCheck), MaxCheckOutput)
	}
}
