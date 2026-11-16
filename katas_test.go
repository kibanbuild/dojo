package dojo

import (
	"io/fs"
	"path"
	"testing"
)

// TestBinaryCarriesNoSolutions fails if anything other than the three runtime
// files of a kata gets embedded, so a solution can never ship in the binary.
func TestBinaryCarriesNoSolutions(t *testing.T) {
	allowed := map[string]bool{"kata.yaml": true, "setup.sh": true, "check.sh": true}
	n := 0
	err := fs.WalkDir(Katas(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		n++
		if !allowed[path.Base(p)] {
			t.Errorf("embedded file %s must not ship in the binary", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("nothing embedded: the catalog is empty")
	}
}
