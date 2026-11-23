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
	"net/http"
	"strings"
	"time"

	"github.com/kibanbuild/dojo/internal/catalog"
	"github.com/kibanbuild/dojo/internal/doctor"
	"github.com/kibanbuild/dojo/internal/lab"
	"github.com/kibanbuild/dojo/internal/mentor"
	"github.com/kibanbuild/dojo/internal/sshconfig"
	"github.com/kibanbuild/dojo/internal/state"
)

// Usage is printed by dojo help.
const Usage = `dojo: practise real IT work on a local lab machine.

Usage:
  dojo doctor             check that this computer can run the labs
  dojo start [--key PATH] <role>/<ticket>
                          create a fresh lab and break it for the ticket; also lets your
                          SSH key into the lab and sets up "ssh dojo" (--key picks the
                          key, a private or .pub path, from now on)
  dojo status             show the current ticket and its brief
  dojo ssh                open a terminal inside the lab (or: ssh dojo)
  dojo hint               show the next hint for the current ticket
  dojo check [--quick]    reboot the lab and verify your fix (--quick skips the reboot)
  dojo stop               stop the lab and delete the lab machine
  dojo mcp                run the mentor server your AI tool connects to (see the README)
  dojo login [--force]    sign in with GitHub (an account made on the website first)
  dojo logout [--local]   sign out; --local only forgets the token on this computer
  dojo telemetry [on|off|status]
                          share usage stats (on by default; see the README)

Environment:
  DOJO_BACKEND     lab backend (default lima)
  DOJO_KATAS       read tickets from this catalog directory instead of the built-in one
  DOJO_HOME        where progress and the login token are stored
  DOJO_API_URL     the Dojo API (login, logout)
  DOJO_GITHUB_URL  GitHub's base URL (default https://github.com)
  DOJO_GITHUB_CLIENT_ID  the OAuth App client id login uses
  DOJO_TELEMETRY=off, DO_NOT_TRACK=1
                   never share usage stats, whatever dojo telemetry says
`

// ErrCheckFailed means exit 1; the failure was already explained to the learner.
var ErrCheckFailed = errors.New("not fixed yet")

// ErrNotReady means exit 1; doctor already listed what to fix.
var ErrNotReady = errors.New("this computer is not ready for the labs")

// App holds everything the commands touch.
type App struct {
	Out io.Writer
	// In and Err carry dojo mcp's protocol and logs.
	In      io.Reader
	Err     io.Writer
	Catalog fs.FS
	// BackendName is the configured lab backend; NewBackend builds it.
	BackendName string
	NewBackend  func(name string) (lab.Backend, error)
	Now         func() time.Time
	// Probe reads the host's facts for dojo doctor.
	Probe func(context.Context) doctor.Machine
	// Login says where login and logout talk to; HTTP and Sleep are replaceable for tests.
	Login LoginConfig
	HTTP  *http.Client
	Sleep func(context.Context, time.Duration) error
	// Getenv reads DOJO_TELEMETRY and DO_NOT_TRACK; nil means the process environment.
	Getenv func(string) string
}

// commands are the names usage stats may record; anything else (help, a typo) is never counted.
var commands = map[string]bool{"status": true, "hint": true, "doctor": true, "start": true, "ssh": true,
	"check": true, "stop": true, "mcp": true, "login": true, "logout": true, "telemetry": true}

// removed are the pre-v1 verbs, gone without aliases (the product has no users yet); a learner who
// types one is told what replaced it.
var removed = map[string]string{
	"shell":      "it is now dojo ssh",
	"clean":      "it is now dojo stop",
	"ssh-config": "dojo start sets up ssh dojo now; pick the key with dojo start --key PATH <role>/<ticket>",
	"list":       "there is no list command; start a ticket with dojo start <role>/<ticket>",
}

// Run executes one command line (without the program name) and, around it, records the command's
// name, result and duration for usage stats. Only the name is kept, never an argument.
func (a *App) Run(ctx context.Context, args []string) error {
	if len(args) == 0 || !commands[args[0]] {
		return a.run(ctx, args)
	}
	name := args[0]
	record := a.telemetryBegin(name)
	t0 := a.now()
	err := a.run(ctx, args)
	if record {
		a.telemetryEnd(ctx, name, a.now().Sub(t0), err)
	}
	return err
}

func (a *App) run(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(a.Out, Usage)
		return nil
	}
	cmd, rest := args[0], args[1:]
	if !commands[cmd] {
		if hint, ok := removed[cmd]; ok {
			return fmt.Errorf("unknown command %q: %s (see dojo help)", cmd, hint)
		}
		return fmt.Errorf("unknown command %q (see dojo help)", cmd)
	}
	fset := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fset.SetOutput(io.Discard)
	quick, key := new(bool), new(string)
	var lf *loginFlags
	switch cmd {
	case "login", "logout":
		lf = newLoginFlags(cmd, fset)
	case "check":
		quick = fset.Bool("quick", false, "skip the reboot")
	case "start":
		key = fset.String("key", "", "SSH key to use")
	}
	err := fset.Parse(rest)
	var positional []string
	if err == nil {
		positional = fset.Args()
	}
	if err == nil && cmd == "start" && len(positional) > 1 {
		// Flags may follow the ticket: dojo start <ticket> --key PATH.
		if err = fset.Parse(positional[1:]); err == nil {
			positional = append(positional[:1:1], fset.Args()...)
		}
	}
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(a.Out, Usage)
		return nil
	} else if err != nil {
		return fmt.Errorf("%w (see dojo help)", err)
	}
	emptyKey := false
	fset.Visit(func(f *flag.Flag) { emptyKey = emptyKey || (f.Name == "key" && f.Value.String() == "") })
	if emptyKey {
		return errors.New("--key needs a path (see dojo help)")
	}
	want := 0
	if cmd == "start" {
		want = 1
	}
	if cmd == "telemetry" {
		return a.telemetryCmd(ctx, positional)
	}
	if len(positional) != want {
		if cmd == "start" {
			return errors.New("usage: dojo start [--key PATH] <role>/<ticket>")
		}
		return fmt.Errorf("usage: dojo %s takes no arguments (see dojo help)", cmd)
	}

	switch cmd {
	case "doctor":
		return a.doctor(ctx)
	case "status":
		return a.status()
	case "hint":
		return a.hint()
	case "mcp":
		return a.mcp(ctx)
	case "login":
		return a.login(ctx, lf)
	case "logout":
		return a.logout(ctx, lf)
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
		return a.start(ctx, backend, positional[0], *key)
	case "ssh":
		return a.ssh(ctx, backend)
	case "check":
		return a.check(ctx, backend, *quick)
	default: // stop
		fmt.Fprintln(a.Out, "Stopping the lab: deleting the lab machine...")
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

func (a *App) current() (*state.State, *catalog.Ticket, error) {
	st, err := a.load()
	if err != nil {
		return nil, nil, err
	}
	if st.Current == "" {
		return nil, nil, errors.New("no ticket in progress: run dojo start <role>/<ticket>")
	}
	k, err := catalog.LoadTicket(a.Catalog, st.Current)
	return st, k, err
}

// start creates the lab for a ticket and sets up ssh dojo. key is the --key flag: empty keeps the
// remembered key, and a key that cannot be used stops the command before the lab is touched.
func (a *App) start(ctx context.Context, backend lab.Backend, id, key string) error {
	k, err := catalog.LoadTicket(a.Catalog, id)
	if err != nil {
		return err
	}
	if key != "" {
		dir, err := sshconfig.Dir()
		if err == nil {
			_, _, err = sshconfig.ResolveKey(dir, key)
		}
		if err != nil {
			return fmt.Errorf("%w (pick another key with dojo start --key <path> %s)", err, id)
		}
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
		return fmt.Errorf("ticket setup: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("ticket setup failed (exit %d)\n%s", code, out)
	}
	st.Current = k.ID
	if err := a.save(st); err != nil {
		return err
	}
	chosen := st.SSHKey
	if key != "" {
		chosen = key
	}
	if err := a.trySSH(ctx, backend, st, chosen, true); err != nil {
		return err
	}
	if err := a.tryMentorFiles(ctx, backend); err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "Lab ready in %s.\n\n", a.Now().Sub(t0).Round(time.Second))
	a.printBrief(k)
	fmt.Fprintln(a.Out, "Next: dojo ssh (or ssh dojo) to get in, dojo check when you think it's fixed.")
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

func (a *App) ssh(ctx context.Context, backend lab.Backend) error {
	if _, _, err := a.current(); err != nil {
		return err
	}
	fmt.Fprintln(a.Out, "You're in the lab. Use sudo for admin tasks; type exit to leave.")
	return backend.Shell(ctx)
}

// trySSH sets up ssh dojo but never blocks the ticket, since dojo ssh works without it. chosen is
// the key to use (empty auto-detects); it is remembered only once ssh dojo works with it, so a
// bad key never leaves a broken setting behind. Only an interrupt is returned.
func (a *App) trySSH(ctx context.Context, backend lab.Backend, st *state.State, chosen string, announce bool) error {
	priv, err := a.setupSSH(ctx, backend, chosen, announce)
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	if err == nil {
		err = a.rememberKey(st, priv)
	}
	if err != nil {
		fmt.Fprintf(a.Out, "Warning: ssh dojo is not set up: %v\nUse dojo ssh, or fix that and run dojo start again.\n", err)
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
		return "", fmt.Errorf("%w (pick another key with dojo start --key <path> <role>/<ticket>)", err)
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
		fmt.Fprintf(a.Out, "ssh dojo is ready, using your key %s (change it: dojo start --key <path> <role>/<ticket>).\n", priv)
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
		if err := a.trySSH(ctx, backend, st, st.SSHKey, false); err != nil {
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
	fmt.Fprintf(a.Out, "\nTicket passed: %s.\n", k.Title)
	return nil
}

// tryMentorFiles writes the mentor rules into the learner's home in the lab, where
// an AI tool run through ssh dojo or VS Code Remote reads them. Like ssh dojo it
// never blocks the ticket; only an interrupt is returned.
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
	fmt.Fprintln(a.Out, "\nReady. Next: dojo start <role>/<ticket>.")
	return nil
}

func (a *App) printBrief(k *catalog.Ticket) {
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
