package catalog_test

import (
	"testing"

	"github.com/kibanyadev/dojo/internal/catalog"
)

func TestValidTicketID(t *testing.T) {
	for id, want := range map[string]bool{
		"support-l1/perms-03": true,
		"a/b":                 true,
		"":                    false,
		"a":                   false,
		"a/b/c":               false,
		"/b":                  false,
		"a/":                  false,
		"A/b":                 false,
		"a_b/c":               false,
		"a--b/c":              false,
		"a/b\n":               false,
		"a/b/../c":            false,
	} {
		if got := catalog.ValidTicketID(id); got != want {
			t.Errorf("ValidTicketID(%q) = %v, want %v", id, got, want)
		}
	}
}
