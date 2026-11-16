// Package cli implements the dojo commands. Dependencies are injected so every
// command can be tested without a VM.
package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"time"

	"github.com/kibanbuild/dojo/internal/kata"
	"github.com/kibanbuild/dojo/internal/lab"
	"github.com/kibanbuild/dojo/internal/state"
)

// Usage is printed by dojo help.
const Usage = `dojo: practise real IT work on a local lab machine.

Usage:
  dojo list               show the available katas
  dojo start <kata>       create a fresh lab and break it for <kata>
  dojo status             show the current kata and its brief
  dojo shell              open a terminal inside the lab
  dojo hint               show the next hint for the current kata
  dojo check [--quick]    reboot the lab and verify your fix (--quick skips the reboot)
  dojo clean              delete the lab machine

Environment:
  DOJO_BACKEND     lab backend (default lima)
  DOJO_KATAS       read katas from this directory instead of the built-in catalog
  DOJO_HOME        where progress is stored
`

// ErrCheckFailed means exit 1; the failure was already explained to the learner.
var ErrCheckFailed = errors.New("not fixed yet")

// App holds everything the commands touch.
type App struct {
	Out     io.Writer
	Catalog fs.FS
	// BackendName is the configured lab backend; NewBackend builds it.
	BackendName string
	NewBackend  func(name string) (lab.Backend, error)
	Now         func() time.Time
}

// Run executes one command line (without the program name).
func (a *App) Run(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(a.Out, Usage)
		return nil
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "list", "status", "hint", "start", "shell", "check", "clean":
	default:
		return fmt.Errorf("unknown command %q (see dojo help)", cmd)
	}
	fset := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fset.SetOutput(io.Discard)
	quick := new(bool)
	if cmd == "check" {
		quick = fset.Bool("quick", false, "skip the reboot")
	}
	if err := fset.Parse(rest); errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(a.Out, Usage)
		return nil
	} else if err != nil {
		return fmt.Errorf("%w (see dojo help)", err)
	}
	want := 0
	if cmd == "start" {
		want = 1
	}
	if fset.NArg() != want {
		if cmd == "start" {
			return errors.New("usage: dojo start <kata>  (see dojo list)")
		}
		return fmt.Errorf("usage: dojo %s takes no arguments (see dojo help)", cmd)
	}

	switch cmd {
	case "list":
		return a.list()
	case "status":
		return a.status()
	case "hint":
		return a.hint()
	}
	backend, err := a.NewBackend(a.BackendName)
	if err != nil {
		return err
	}
	if err := backend.Available(); err != nil {
		return err
	}
	switch cmd {
	case "start":
		return a.start(ctx, backend, fset.Arg(0))
	case "shell":
		return a.shell(ctx, backend)
	case "check":
		return a.check(ctx, backend, *quick)
	default: // clean
		fmt.Fprintln(a.Out, "Deleting the lab machine...")
		if err := backend.Delete(ctx); err != nil {
			return err
		}
		st, err := a.load()
		if err != nil {
			return err
		}
		st.Current, st.HintsShown = "", 0
		return a.save(st)
	}
}

// load and save add the state file path and a remedy to any state error.
func (a *App) load() (*state.State, error) {
	st, err := state.Load()
	if err != nil {
		return nil, stateErr(err)
	}
	return st, nil
}

func (a *App) save(st *state.State) error {
	if err := st.Save(); err != nil {
		return stateErr(err)
	}
	return nil
}

func stateErr(err error) error {
	p, perr := state.Path()
	if perr != nil {
		p = "(unknown)"
	}
	return fmt.Errorf("progress file %s: %w (fix its permissions or remove it; DOJO_HOME moves it)", p, err)
}

func (a *App) list() error {
	katas, err := kata.List(a.Catalog)
	if err != nil {
		return err
	}
	st, err := a.load()
	if err != nil {
		return err
	}
	for _, k := range katas {
		mark := " "
		if _, ok := st.Passed[k.ID]; ok {
			mark = "✓"
		}
		fmt.Fprintf(a.Out, "%s %-24s %s\n", mark, k.ID, k.Summary)
	}
	return nil
}

func (a *App) current() (*state.State, *kata.Kata, error) {
	st, err := a.load()
	if err != nil {
		return nil, nil, err
	}
	if st.Current == "" {
		return nil, nil, errors.New("no kata in progress: run dojo list, then dojo start <kata>")
	}
	k, err := kata.Load(a.Catalog, st.Current)
	return st, k, err
}

func (a *App) start(ctx context.Context, backend lab.Backend, id string) error {
	k, err := kata.Load(a.Catalog, id)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "Starting a fresh lab for %s (%s).\n", k.ID, backend.Name())
	fmt.Fprintln(a.Out, "The first time it downloads Ubuntu, so it can take a few minutes.")
	t0 := a.Now()
	st, err := a.load()
	if err != nil {
		return err
	}
	st.Current, st.HintsShown = "", 0
	if err := a.save(st); err != nil {
		return err
	}
	if err := backend.Recreate(ctx); err != nil {
		return orCanceled(ctx, err)
	}
	out, code, err := backend.Run(ctx, k.Setup)
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	if err != nil {
		return fmt.Errorf("kata setup: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("kata setup failed (exit %d)\n%s", code, out)
	}
	st.Current = k.ID
	if err := a.save(st); err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "Lab ready in %s.\n\n", a.Now().Sub(t0).Round(time.Second))
	a.printBrief(k)
	fmt.Fprintln(a.Out, "Next: dojo shell to get in, dojo check when you think it's fixed.")
	return nil
}

func (a *App) status() error {
	st, k, err := a.current()
	if err != nil {
		return err
	}
	a.printBrief(k)
	if t, ok := st.Passed[k.ID]; ok {
		fmt.Fprintf(a.Out, "Passed on %s.\n", t.Format("2006-01-02"))
	}
	return nil
}

func (a *App) hint() error {
	st, k, err := a.current()
	if err != nil {
		return err
	}
	if st.HintsShown < 0 {
		st.HintsShown = 0
	}
	if st.HintsShown >= len(k.Hints) {
		fmt.Fprintln(a.Out, "No more hints. Read the man pages and try a different angle.")
		return nil
	}
	fmt.Fprintf(a.Out, "Hint %d/%d: %s\n", st.HintsShown+1, len(k.Hints), k.Hints[st.HintsShown])
	st.HintsShown++
	return a.save(st)
}

func (a *App) shell(ctx context.Context, backend lab.Backend) error {
	if _, _, err := a.current(); err != nil {
		return err
	}
	fmt.Fprintln(a.Out, "You're in the lab. Use sudo for admin tasks; type exit to leave.")
	return backend.Shell(ctx)
}

func (a *App) check(ctx context.Context, backend lab.Backend, quick bool) error {
	st, k, err := a.current()
	if err != nil {
		return err
	}
	if !quick {
		fmt.Fprintln(a.Out, "Rebooting the lab: a real fix survives a restart...")
		if err := backend.Reboot(ctx); err != nil {
			return orCanceled(ctx, err)
		}
	}
	out, code, err := backend.Run(ctx, k.Check)
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	if err != nil {
		return err
	}
	a.printResults(out)
	if code != 0 {
		fmt.Fprintln(a.Out, "\nNot fixed yet. Keep going, or try dojo hint.")
		return ErrCheckFailed
	}
	if quick {
		fmt.Fprintln(a.Out, "\nLooks fixed. Run dojo check without --quick to confirm it survives a reboot.")
		return nil
	}
	st.Passed[k.ID] = a.Now()
	if err := a.save(st); err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "\nKata passed: %s.\n", k.Title)
	return nil
}

// orCanceled reports the interruption instead of whatever the killed command said.
func orCanceled(ctx context.Context, err error) error {
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	return err
}

func (a *App) printBrief(k *kata.Kata) {
	fmt.Fprintf(a.Out, "%s: %s\n\n%s\n\n", k.ID, k.Title, strings.TrimSpace(k.Brief))
}

func (a *App) printResults(out []byte) {
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "PASS"):
			fmt.Fprintln(a.Out, "  ✓", strings.TrimSpace(strings.TrimPrefix(line, "PASS")))
		case strings.HasPrefix(line, "FAIL"):
			fmt.Fprintln(a.Out, "  ✗", strings.TrimSpace(strings.TrimPrefix(line, "FAIL")))
		default:
			fmt.Fprintln(a.Out, "   ", line)
		}
	}
}
