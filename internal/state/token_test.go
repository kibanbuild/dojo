package state

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestTokenRoundTripAndModes(t *testing.T) {
	t.Setenv("DOJO_HOME", filepath.Join(t.TempDir(), "home"))
	if _, err := LoadToken(); !errors.Is(err, ErrNoToken) {
		t.Fatalf("empty home: %v", err)
	}
	if err := SaveToken("dojo_one"); err != nil {
		t.Fatal(err)
	}
	if err := SaveToken("dojo_two"); err != nil {
		t.Fatal(err)
	}
	got, err := LoadToken()
	if err != nil || got != "dojo_two" {
		t.Fatalf("load: %q %v", got, err)
	}
	p, _ := TokenPath()
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
			t.Errorf("file mode %v, want 0600", fi.Mode().Perm())
		}
		if fi, _ := os.Stat(filepath.Dir(p)); fi.Mode().Perm() != 0o700 {
			t.Errorf("dir mode %v, want 0700", fi.Mode().Perm())
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(p))
	if len(entries) != 1 {
		t.Errorf("temp files left behind: %v", entries)
	}
	if err := RemoveToken(); err != nil {
		t.Fatal(err)
	}
	if err := RemoveToken(); err != nil {
		t.Fatalf("removing twice: %v", err)
	}
	if _, err := LoadToken(); !errors.Is(err, ErrNoToken) {
		t.Fatalf("after remove: %v", err)
	}
}

func TestSaveTokenTightensALoosePreexistingFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	t.Setenv("DOJO_HOME", t.TempDir())
	p, _ := TokenPath()
	if err := os.WriteFile(p, []byte("old"), 0o644); err != nil { //nolint:gosec // the loose file is the fixture
		t.Fatal(err)
	}
	if err := SaveToken("dojo_new"); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", fi.Mode().Perm())
	}
}
