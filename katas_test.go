package dojo

import (
	"io/fs"
	"path"
	"testing"

	"github.com/kibanbuild/dojo/internal/catalog"
)

func TestEmbeddedCatalogIsValid(t *testing.T) {
	c, err := catalog.Load(Katas())
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Tickets) == 0 {
		t.Fatal("embedded catalog has no tickets")
	}
	for _, k := range c.Tickets {
		if len(k.Setup) == 0 || len(k.Check) == 0 {
			t.Errorf("%s: needs setup.sh and check.sh", k.ID)
		}
	}
}

// TestBinaryCarriesNoSolutions fails if anything other than the three runtime
// files of a ticket (and catalog.yaml) gets embedded, so a solution can never ship in the binary.
func TestBinaryCarriesNoSolutions(t *testing.T) {
	allowed := map[string]bool{"catalog.yaml": true, "ticket.yaml": true, "setup.sh": true, "check.sh": true}
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
