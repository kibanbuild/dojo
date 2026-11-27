package main

import (
	"testing"

	cat "github.com/kibanyadev/dojo/internal/catalog"
)

func TestCatalogReadsDojoKatasOverride(t *testing.T) {
	t.Setenv("DOJO_KATAS", "../../internal/catalog/testdata/catalog")
	tk, err := cat.LoadTicket(catalog(), "support-l1/perms-01")
	if err != nil {
		t.Fatal(err)
	}
	if tk.Role != "support-l1" || len(tk.Hints) != 3 {
		t.Errorf("loaded %+v", tk)
	}
}

func TestCatalogDefaultsToTheEmbeddedExample(t *testing.T) {
	t.Setenv("DOJO_KATAS", "")
	if _, err := cat.LoadTicket(catalog(), "example/motd-01"); err != nil {
		t.Fatal(err)
	}
}
