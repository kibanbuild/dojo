package cli

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/kibanbuild/dojo/internal/lab"
	"github.com/kibanbuild/dojo/internal/state"
)

// scriptLab runs nothing: it keeps every script the CLI sends, so a test can see which
// setup.sh and check.sh came from the catalog tree.
type scriptLab struct {
	*fakeLab
	scripts []string
	check   int
}

func (s *scriptLab) Run(ctx context.Context, script []byte) ([]byte, int, error) {
	if strings.Contains(string(script), "AGENTS.md") {
		return s.fakeLab.Run(ctx, script)
	}
	s.scripts = append(s.scripts, string(script))
	if strings.Contains(string(script), "PASS") || strings.Contains(string(script), "FAIL") {
		return []byte("PASS  fixture\n"), s.check, nil
	}
	return []byte("setup output"), 0, nil
}

// The fixture tree in internal/catalog/testdata is read the way DOJO_KATAS reads a directory.
func TestStartAndCheckFromACatalogTree(t *testing.T) {
	t.Setenv("DOJO_KATAS", "../catalog/testdata/catalog")
	e := newEnv(t)
	sl := &scriptLab{fakeLab: e.lab}
	e.app.NewBackend = func(string) (lab.Backend, error) { return sl, nil }
	e.app.Catalog = os.DirFS(os.Getenv("DOJO_KATAS"))

	if err := e.run(t, "start", "support-l1/perms-01"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.out.String(), "Ada cannot write to /srv/shared") {
		t.Errorf("brief not shown:\n%s", e.out)
	}
	if len(sl.scripts) == 0 || !strings.Contains(sl.scripts[0], "mkdir -p /srv/shared") {
		t.Errorf("setup.sh of the ticket was not run: %q", sl.scripts)
	}
	if st, _ := state.Load(); st.Current != "support-l1/perms-01" {
		t.Errorf("current = %q", st.Current)
	}

	sl.check = 1
	if err := e.run(t, "check", "--quick"); !errors.Is(err, ErrCheckFailed) {
		t.Fatalf("failing check: %v", err)
	}
	sl.check = 0
	if err := e.run(t, "check", "--quick"); err != nil || !strings.Contains(e.out.String(), "Looks fixed") {
		t.Fatalf("passing check: %v\n%s", err, e.out)
	}
	if !strings.Contains(sl.scripts[len(sl.scripts)-1], "staff can write to /srv/shared") {
		t.Errorf("check.sh of the ticket was not run: %q", sl.scripts[len(sl.scripts)-1])
	}

	if err := e.run(t, "hint"); err != nil || !strings.Contains(e.out.String(), "Hint 1/3: Look at who owns the directory.") {
		t.Errorf("hint: %v\n%s", err, e.out)
	}
}

func TestStartRefusesABrokenTicketInATree(t *testing.T) {
	e := newEnv(t)
	e.app.Catalog = os.DirFS("../catalog/testdata/catalog")
	for _, id := range []string{"support-l1/missing", "../catalog", "foundations"} {
		if err := e.run(t, "start", id); err == nil {
			t.Errorf("start %q should fail", id)
		}
	}
	if e.lab.has("recreate") {
		t.Error("lab recreated for an unknown ticket")
	}
}
