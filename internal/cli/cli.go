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

	"github.com/kibanbuild/dojo/internal/doctor"
	"github.com/kibanbuild/dojo/internal/kata"
	"github.com/kibanbuild/dojo/internal/lab"
	"github.com/kibanbuild/dojo/internal/mentor"
	"github.com/kibanbuild/dojo/internal/sshconfig"
	"github.com/kibanbuild/dojo/internal/state"
)

// Usage is printed by dojo help.
const Usage = `dojo: practise real IT work on a local lab machine.

Usage:
  dojo doctor             check that this computer can run the labs
  dojo list               show the available katas
  dojo start <kata>       create a fresh lab and break it for <kata>
  dojo status             show the current kata and its brief
  dojo shell              open a terminal inside the lab (or: ssh dojo)
  dojo ssh-config [--key PATH]
                          let your SSH key into the lab and set up "ssh dojo";
                          --key picks the key (private or .pub path) from now on
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

// ErrNotReady means exit 1; doctor already listed what to fix.
var ErrNotReady = errors.New("this computer is not ready for the labs")

// App holds everything the commands touch.
type App struct {
	Out     io.Writer
	Catalog fs.FS
	// BackendName is the configured lab backend; NewBackend builds it.
	BackendName string
	NewBackend  func(name string) (lab.Backend, error)
	Now         func() time.Time
	// Probe reads the host's facts for dojo doctor.
	Probe func(context.Context) doctor.Machine
}

// Run executes one command line (without the program name).
func (a *App) Run(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(a.Out, Usage)
		return nil
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "list", "status", "hint", "doctor", "start", "shell", "ssh-config", "check", "clean":
	default:
		return fmt.Errorf("unknown command %q (see dojo help)", cmd)
	}
	fset := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fset.SetOutput(io.Discard)
	quick, key := new(bool), new(string)
	switch cmd {
	case "check":
		quick = fset.Bool("quick", false, "skip the reboot")
	case "ssh-config":
		key = fset.String("key", "", "SSH key to use")
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
	case "doctor":
		return a.doctor(ctx)
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
	case "ssh-config":
		return a.sshConfig(ctx, backend, *key)
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
		st.Current, st.HintsShown, st.LastCheck = "", 0, ""
		if err := a.save(st); err != nil {
			return err
		}
		return a.removeSSH()
	}
}

// removeSSH empties the ssh dojo entry once the lab is gone, so it never keeps a
// dead port. It runs after the delete: while the delete fails the lab may still
// exist and the entry still tells the truth. The learner's own config stays as is.
func (a *App) removeSSH() error {
	dir, err := sshconfig.Dir()
	if err == nil {
		err = sshconfig.Remove(dir)
	}
	if err != nil {
		return fmt.Errorf("the lab is deleted but ssh dojo could not be cleared: %w (empty %s by hand)", err, sshconfig.FileName)
	}
	return nil
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
	st.Current, st.HintsShown, st.LastCheck = "", 0, ""
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
	if err := a.trySSH(ctx, backend, st, true); err != nil {
		return err
	}
	if err := a.tryMentorFiles(ctx, backend); err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "Lab ready in %s.\n\n", a.Now().Sub(t0).Round(time.Second))
	a.printBrief(k)
	fmt.Fprintln(a.Out, "Next: dojo shell (or ssh dojo) to get in, dojo check when you think it's fixed.")
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

// sshConfig sets up ssh dojo on demand. The key is remembered only once ssh dojo
// works with it, so a typo or a bad key never leaves a broken setting behind.
func (a *App) sshConfig(ctx context.Context, backend lab.Backend, key string) error {
	st, err := a.load()
	if err != nil {
		return err
	}
	exists, err := backend.Exists(ctx)
	if err != nil {
		return orCanceled(ctx, err)
	}
	if !exists {
		return errors.New("no lab machine yet: run dojo start <kata> first")
	}
	if key == "" {
		key = st.SSHKey
	}
	priv, err := a.setupSSH(ctx, backend, key, true)
	if err != nil {
		return orCanceled(ctx, err)
	}
	return a.rememberKey(st, priv)
}

// trySSH sets up ssh dojo but never blocks the kata, since dojo shell works
// without it. Only an interrupt is returned.
func (a *App) trySSH(ctx context.Context, backend lab.Backend, st *state.State, announce bool) error {
	priv, err := a.setupSSH(ctx, backend, st.SSHKey, announce)
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	if err == nil {
		err = a.rememberKey(st, priv)
	}
	if err != nil {
		fmt.Fprintf(a.Out, "Warning: ssh dojo is not set up: %v\nUse dojo shell, or fix that and run dojo ssh-config.\n", err)
	}
	return nil
}

func (a *App) rememberKey(st *state.State, priv string) error {
	if st.SSHKey == priv {
		return nil
	}
	st.SSHKey = priv
	return a.save(st)
}

// setupSSH authorizes the learner's own key in the lab and points "ssh dojo" at
// it, returning the private key it used. chosen is empty to auto-detect.
func (a *App) setupSSH(ctx context.Context, backend lab.Backend, chosen string, announce bool) (string, error) {
	dir, err := sshconfig.Dir()
	if err != nil {
		return "", err
	}
	priv, pub, err := sshconfig.ResolveKey(dir, chosen)
	if errors.Is(err, sshconfig.ErrNoKey) {
		return "", fmt.Errorf("%w in %s: create one with `ssh-keygen -t ed25519 -C \"you@example.com\"`, or pass --key <path>", err, dir)
	}
	if err != nil {
		return "", fmt.Errorf("%w (pick another key with dojo ssh-config --key <path>)", err)
	}
	if err := backend.AuthorizeKey(ctx, pub); err != nil {
		return "", err
	}
	block, err := backend.SSHConfig(ctx, priv)
	if err != nil {
		return "", err
	}
	if err := sshconfig.Install(dir, block); err != nil {
		return "", err
	}
	if announce {
		fmt.Fprintf(a.Out, "ssh dojo is ready, using your key %s (change it: dojo ssh-config --key <path>).\n", priv)
	}
	return priv, nil
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
		// The VM comes back on a new SSH port, so ssh dojo must be pointed at it again.
		if err := a.trySSH(ctx, backend, st, false); err != nil {
			return err
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
	// Kept for the mentor, after the check ran and whatever its verdict.
	st.SetLastCheck(out)
	if code != 0 {
		if err := a.save(st); err != nil {
			return err
		}
		fmt.Fprintln(a.Out, "\nNot fixed yet. Keep going, or try dojo hint.")
		return ErrCheckFailed
	}
	if quick {
		if err := a.save(st); err != nil {
			return err
		}
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

// tryMentorFiles writes the mentor rules into the learner's home in the lab, where
// an AI tool run through ssh dojo or VS Code Remote reads them. Like ssh dojo it
// never blocks the kata; only an interrupt is returned.
func (a *App) tryMentorFiles(ctx context.Context, backend lab.Backend) error {
	out, code, err := backend.Run(ctx, mentorScript())
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	if err == nil && code != 0 {
		err = fmt.Errorf("exit %d: %s", code, strings.TrimSpace(string(out)))
	}
	if err != nil {
		fmt.Fprintf(a.Out, "Warning: the mentor rules (AGENTS.md) were not written in the lab: %v\n", err)
	}
	return nil
}

// mentorScript runs as root and writes AGENTS.md and CLAUDE.md into the lab
// user's home. Run goes through sudo, so SUDO_USER names that user whatever
// its uid or home.
func mentorScript() []byte {
	var b strings.Builder
	b.WriteString(`set -eu
user="${SUDO_USER:-}"
[ -n "$user" ] && [ "$user" != root ] || { echo "no lab user: the script must run through sudo from the lab user" >&2; exit 1; }
home=$(getent passwd "$user" | cut -d: -f6)
[ -n "$home" ] && [ -d "$home" ] || { echo "no lab user home found for $user" >&2; exit 1; }
cat > "$home/AGENTS.md" <<'DOJO_AGENTS_EOF'
`)
	b.WriteString(mentor.AgentsMD())
	b.WriteString("DOJO_AGENTS_EOF\nprintf '%s\\n' '" + strings.TrimSuffix(mentor.ClaudeMD, "\n") + "' > \"$home/CLAUDE.md\"\n")
	b.WriteString("chown \"$user:$(id -gn \"$user\")\" \"$home/AGENTS.md\" \"$home/CLAUDE.md\"\n")
	return []byte(b.String())
}

// orCanceled reports the interruption instead of whatever the killed command said.
func orCanceled(ctx context.Context, err error) error {
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	return err
}

func (a *App) doctor(ctx context.Context) error {
	backend, err := a.NewBackend(a.BackendName)
	if err == nil {
		err = backend.Available()
	}
	m := a.Probe(ctx)
	var chosen string
	if st, serr := a.load(); serr == nil { // a corrupt state file is reported by the other commands
		chosen = st.SSHKey
	}
	doctor.ProbeSSH(&m, chosen)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	results := doctor.Evaluate(m, a.BackendName, err)
	marks := map[doctor.Status]string{doctor.OK: "✓", doctor.Warn: "!", doctor.Fail: "✗"}
	for _, r := range results {
		fmt.Fprintf(a.Out, "  %s %s\n", marks[r.Status], r.Text)
		if r.Fix != "" && r.Status != doctor.OK {
			fmt.Fprintf(a.Out, "      -> %s\n", strings.ReplaceAll(r.Fix, "\n", "\n         "))
		}
	}
	if !doctor.Ready(results) {
		fmt.Fprintln(a.Out, "\nNot ready yet. Fix the ✗ items and run dojo doctor again.")
		return ErrNotReady
	}
	fmt.Fprintln(a.Out, "\nReady. Next: dojo list, then dojo start <kata>.")
	return nil
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
