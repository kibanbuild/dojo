// Package kata loads kata definitions from a catalog.
//
// A kata is a directory <belt>/<name>/ holding kata.yaml, setup.sh and check.sh.
package kata

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Kata is one exercise: what to tell the learner and how to break and verify the lab.
type Kata struct {
	ID      string   `yaml:"id"`
	Title   string   `yaml:"title"`
	Belt    string   `yaml:"belt"`
	Summary string   `yaml:"summary"`
	Brief   string   `yaml:"brief"`
	Hints   []string `yaml:"hints"`

	Setup []byte `yaml:"-"`
	Check []byte `yaml:"-"`
}

// ErrNotFound means the catalog has no kata with that id.
var ErrNotFound = errors.New("kata not found")

// Load reads the kata with the given id ("white/permissions-01").
func Load(catalog fs.FS, id string) (*Kata, error) {
	if !fs.ValidPath(id) || strings.Count(id, "/") != 1 {
		return nil, fmt.Errorf("%w: %q (expected <belt>/<name>)", ErrNotFound, id)
	}
	raw, err := fs.ReadFile(catalog, path.Join(id, "kata.yaml"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return nil, err
	}
	// Unknown keys are errors, so a typo like "hint:" can't silently drop the hints.
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var k Kata
	if err := dec.Decode(&k); err != nil {
		return nil, fmt.Errorf("%s/kata.yaml: %w", id, err)
	}
	if k.ID != id {
		return nil, fmt.Errorf("%s/kata.yaml: id is %q, want %q", id, k.ID, id)
	}
	if belt := strings.SplitN(id, "/", 2)[0]; k.Belt != belt {
		return nil, fmt.Errorf("%s/kata.yaml: belt is %q, want %q", id, k.Belt, belt)
	}
	if k.Title == "" || k.Brief == "" {
		return nil, fmt.Errorf("%s/kata.yaml: title and brief are required", id)
	}
	if k.Setup, err = fs.ReadFile(catalog, path.Join(id, "setup.sh")); err != nil {
		return nil, fmt.Errorf("%s: %w", id, err)
	}
	if k.Check, err = fs.ReadFile(catalog, path.Join(id, "check.sh")); err != nil {
		return nil, fmt.Errorf("%s: %w", id, err)
	}
	// A Windows checkout may turn LF into CRLF; bash would choke on the \r.
	k.Setup = bytes.ReplaceAll(k.Setup, []byte("\r\n"), []byte("\n"))
	k.Check = bytes.ReplaceAll(k.Check, []byte("\r\n"), []byte("\n"))
	return &k, nil
}

// List returns every kata in the catalog, sorted by id.
func List(catalog fs.FS) ([]*Kata, error) {
	ids, err := fs.Glob(catalog, "*/*/kata.yaml")
	if err != nil {
		return nil, err
	}
	sort.Strings(ids)
	katas := make([]*Kata, 0, len(ids))
	for _, p := range ids {
		k, err := Load(catalog, path.Dir(p))
		if err != nil {
			return nil, err
		}
		katas = append(katas, k)
	}
	return katas, nil
}
