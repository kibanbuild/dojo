package catalog

import (
	"os"
	"path/filepath"
	"testing"
)

// A write that fails half way leaves dst as it was found, so a retry works.
func TestWriteEntriesCleansUpAfterAFailure(t *testing.T) {
	// "x" is a file, then "x/y" needs it to be a directory: the second write fails.
	entries := []entry{
		{name: "a.txt", data: []byte("a")},
		{name: "d", dir: true},
		{name: "x", data: []byte("x")},
		{name: "x/y", data: []byte("y")},
	}
	good := []entry{{name: "a.txt", data: []byte("a")}, {name: "d", dir: true}, {name: "x", data: []byte("x")}}

	t.Run("absent dst is removed", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "lab")
		if err := writeEntries(entries, dst); err == nil {
			t.Fatal("want an error")
		}
		if _, err := os.Lstat(dst); !os.IsNotExist(err) {
			t.Errorf("dst should be gone, Lstat = %v", err)
		}
		if err := writeEntries(good, dst); err != nil {
			t.Errorf("retry: %v", err)
		}
	})

	t.Run("empty dst is emptied", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "lab")
		if err := os.Mkdir(dst, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := writeEntries(entries, dst); err == nil {
			t.Fatal("want an error")
		}
		if ents, err := os.ReadDir(dst); err != nil || len(ents) != 0 {
			t.Errorf("dst should exist and be empty: %v, %d entries", err, len(ents))
		}
		if err := writeEntries(good, dst); err != nil {
			t.Errorf("retry: %v", err)
		}
	})

	t.Run("non-empty dst is left alone", func(t *testing.T) {
		dst := t.TempDir()
		keep := filepath.Join(dst, "keep")
		if err := os.WriteFile(keep, []byte("mine"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := writeEntries(good, dst); err == nil {
			t.Fatal("want an error")
		}
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("a refused extraction must not touch existing files: %v", err)
		}
	})
}
