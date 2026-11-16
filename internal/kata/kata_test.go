package kata_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/kibanbuild/dojo"
	"github.com/kibanbuild/dojo/internal/kata"
)

const validYAML = "id: white/a\ntitle: T\nbelt: white\nsummary: s\nbrief: B\nhints: [h]\n"

// catalog builds a one-kata catalog; files override or remove (empty string) entries.
func catalog(files map[string]string) fstest.MapFS {
	base := map[string]string{
		"white/a/kata.yaml": validYAML,
		"white/a/setup.sh":  "setup",
		"white/a/check.sh":  "check",
	}
	for k, v := range files {
		base[k] = v
	}
	fsys := fstest.MapFS{}
	for k, v := range base {
		if v != "" {
			fsys[k] = &fstest.MapFile{Data: []byte(v)}
		}
	}
	return fsys
}

func TestEmbeddedCatalogIsValid(t *testing.T) {
	katas, err := kata.List(dojo.Katas())
	if err != nil {
		t.Fatal(err)
	}
	if len(katas) == 0 {
		t.Fatal("embedded catalog has no katas")
	}
	for _, k := range katas {
		if len(k.Setup) == 0 || len(k.Check) == 0 || len(k.Hints) == 0 {
			t.Errorf("%s: needs setup.sh, check.sh and hints", k.ID)
		}
	}
}

func TestLoadValid(t *testing.T) {
	k, err := kata.Load(catalog(nil), "white/a")
	if err != nil {
		t.Fatal(err)
	}
	if k.Title != "T" || k.Belt != "white" || string(k.Setup) != "setup" || string(k.Check) != "check" || len(k.Hints) != 1 {
		t.Errorf("loaded %+v", k)
	}
}

func TestLoadNormalisesCRLF(t *testing.T) {
	k, err := kata.Load(catalog(map[string]string{
		"white/a/setup.sh": "echo a\r\necho b\r\n",
		"white/a/check.sh": "exit 0\r\n",
	}), "white/a")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.ContainsRune(k.Setup, '\r') || bytes.ContainsRune(k.Check, '\r') {
		t.Errorf("CRLF survived: setup %q, check %q", k.Setup, k.Check)
	}
}

func TestLoadRejectsBadIDs(t *testing.T) {
	for _, id := range []string{"", "white", "../etc/passwd", "white/a/b", "/white/a", "white/missing"} {
		if _, err := kata.Load(catalog(nil), id); !errors.Is(err, kata.ErrNotFound) {
			t.Errorf("Load(%q) error = %v, want ErrNotFound", id, err)
		}
	}
}

func TestLoadRejectsBrokenKatas(t *testing.T) {
	tests := map[string]map[string]string{
		"id mismatch":   {"white/a/kata.yaml": strings.Replace(validYAML, "id: white/a", "id: white/b", 1)},
		"belt mismatch": {"white/a/kata.yaml": strings.Replace(validYAML, "belt: white", "belt: yellow", 1)},
		"missing title": {"white/a/kata.yaml": strings.Replace(validYAML, "title: T\n", "", 1)},
		"missing brief": {"white/a/kata.yaml": strings.Replace(validYAML, "brief: B\n", "", 1)},
		"unknown key":   {"white/a/kata.yaml": strings.Replace(validYAML, "hints:", "hint:", 1)},
		"invalid yaml":  {"white/a/kata.yaml": "id: [unclosed\n"},
		"missing setup": {"white/a/setup.sh": ""},
		"missing check": {"white/a/check.sh": ""},
	}
	for name, files := range tests {
		k, err := kata.Load(catalog(files), "white/a")
		if err == nil {
			t.Errorf("%s: loaded %+v, want an error", name, k)
		} else if errors.Is(err, kata.ErrNotFound) {
			t.Errorf("%s: got ErrNotFound for a kata that exists but is broken: %v", name, err)
		}
	}
}

func TestListIsSortedAndFailsOnBrokenKata(t *testing.T) {
	fsys := catalog(nil)
	for _, id := range []string{"yellow/z", "white/c", "white/b"} {
		belt := strings.SplitN(id, "/", 2)[0]
		fsys[id+"/kata.yaml"] = &fstest.MapFile{Data: []byte("id: " + id + "\ntitle: T\nbelt: " + belt + "\nbrief: B\n")}
		fsys[id+"/setup.sh"] = &fstest.MapFile{Data: []byte("s")}
		fsys[id+"/check.sh"] = &fstest.MapFile{Data: []byte("c")}
	}
	katas, err := kata.List(fsys)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, k := range katas {
		ids = append(ids, k.ID)
	}
	if got := strings.Join(ids, " "); got != "white/a white/b white/c yellow/z" {
		t.Errorf("List order = %s", got)
	}

	delete(fsys, "white/b/check.sh")
	if _, err := kata.List(fsys); err == nil {
		t.Error("List should fail when one kata is broken, not skip it")
	}
}
