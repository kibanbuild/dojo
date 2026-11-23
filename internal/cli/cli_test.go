package cli

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/kibanbuild/dojo/internal/doctor"
	"github.com/kibanbuild/dojo/internal/lab"
	"github.com/kibanbuild/dojo/internal/state"
)

// fakeLab records calls and answers check.sh with checkCode.
type fakeLab struct {
	calls                  []string
	checkCode              int
	notReady               error
	recreateErr, deleteErr error
	setupCode              int
	onRun                  func()
	// SSH: errors to return, the keys the lab was given, and a lab that does not exist.
	authorizeErr, configErr error
	onAuthorize             func()
	noLab                   bool
	authorized              [][]byte
	// checkOut overrides check.sh's output; checkErr fails the run itself.
	checkOut []byte
	checkErr error
	// mentorCode and mentorErr answer the AGENTS.md script; mentorScripts keeps what it was sent.
	mentorCode    int
	mentorErr     error
	mentorScripts [][]byte
	configKeys    []string
}

func (f *fakeLab) Name() string      { return "fake" }
func (f *fakeLab) Available() error  { return f.notReady }
func (f *fakeLab) record(c string)   { f.calls = append(f.calls, c) }
func (f *fakeLab) has(c string) bool { return strings.Contains(strings.Join(f.calls, " "), c) }

func (f *fakeLab) Recreate(context.Context) error       { f.record("recreate"); return f.recreateErr }
func (f *fakeLab) Exists(context.Context) (bool, error) { return !f.noLab, nil }
func (f *fakeLab) Reboot(context.Context) error         { f.record("reboot"); return nil }
func (f *fakeLab) Shell(context.Context) error          { f.record("shell"); return nil }
func (f *fakeLab) Delete(context.Context) error         { f.record("delete"); return f.deleteErr }
func (f *fakeLab) AuthorizeKey(_ context.Context, pub []byte) error {
	f.record("authorize")
	f.authorized = append(f.authorized, pub)
	if f.onAuthorize != nil {
		f.onAuthorize()
	}
	return f.authorizeErr
}
func (f *fakeLab) SSHConfig(_ context.Context, identityFile string) (string, error) {
	f.record("sshconfig")
	f.configKeys = append(f.configKeys, identityFile)
	if f.configErr != nil {
		return "", f.configErr
	}
	return "Host dojo\n  IdentityFile \"" + identityFile + "\"\n", nil
}
func (f *fakeLab) Run(_ context.Context, script []byte) ([]byte, int, error) {
	if f.onRun != nil {
		f.onRun()
	}
	switch string(script) {
	case "SETUP":
		f.record("setup")
		return []byte("setup output"), f.setupCode, nil
	case "CHECK":
		f.record("check")
		if f.checkErr != nil {
			return nil, 0, f.checkErr
		}
		if f.checkOut != nil {
			return f.checkOut, f.checkCode, nil
		}
		if f.checkCode != 0 {
			return []byte("PASS  one\nFAIL  two\n"), f.checkCode, nil
		}
		return []byte("PASS  one\nPASS  two\n"), 0, nil
	}
	if strings.Contains(string(script), "AGENTS.md") {
		f.record("mentor")
		f.mentorScripts = append(f.mentorScripts, script)
		return []byte("mentor output"), f.mentorCode, f.mentorErr
	}
	return nil, 99, errors.New("unexpected script")
}

var catalog = fstest.MapFS{
	"white/a/kata.yaml": {Data: []byte("id: white/a\ntitle: Kata A\nbelt: white\nsummary: first\nbrief: Fix A.\nhints: [one, two]\n")},
	"white/a/setup.sh":  {Data: []byte("SETUP")},
	"white/a/check.sh":  {Data: []byte("CHECK")},
}

var goodMachine = doctor.Machine{
	OS: "linux", MemoryBytes: 16 << 30, FreeDiskBytes: 100 << 30, CPUs: 8,
	Virtualization: true, VirtualizationKnown: true,
	HasGit: true, GitName: "Ana", GitEmail: "ana@x.dev", HasVSCode: true,
}

type env struct {
	app *App
	lab *fakeLab
	out *bytes.Buffer
}

func newEnv(t *testing.T) *env {
	t.Helper()
	t.Setenv("DOJO_HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("DOJO_SSH_DIR", filepath.Join(t.TempDir(), "ssh"))
	f := &fakeLab{}
	out := &bytes.Buffer{}
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	app := &App{
		Out: out, Catalog: catalog, BackendName: "fake",
		NewBackend: func(string) (lab.Backend, error) { return f, nil },
		Now:        func() time.Time { return now },
		Probe:      func(context.Context) doctor.Machine { return goodMachine },
	}
	return &env{app: app, lab: f, out: out}
}

func (e *env) run(t *testing.T, args ...string) error {
	t.Helper()
	e.out.Reset()
	return e.app.Run(context.Background(), args)
}

func TestHelpAndUnknownCommand(t *testing.T) {
	e := newEnv(t)
	if err := e.run(t); err != nil || !strings.Contains(e.out.String(), "Usage:") {
		t.Errorf("help: %v\n%s", err, e.out)
	}
	if err := e.run(t, "frobnicate"); err == nil {
		t.Error("unknown command should fail")
	}
	if e.lab.has("recreate") {
		t.Error("unknown command touched the lab")
	}
}

func TestCommandsNeedAKataInProgress(t *testing.T) {
	for _, cmd := range []string{"status", "hint", "ssh", "check"} {
		e := newEnv(t)
		if err := e.run(t, cmd); err == nil || !strings.Contains(err.Error(), "no kata in progress") {
			t.Errorf("%s: err = %v", cmd, err)
		}
	}
}

func TestStartSetsUpLab(t *testing.T) {
	e := newEnv(t)
	if err := e.run(t, "start", "white/a"); err != nil {
		t.Fatal(err)
	}
	if !e.lab.has("recreate setup") {
		t.Errorf("calls = %v, want recreate then setup", e.lab.calls)
	}
	if !strings.Contains(e.out.String(), "Fix A.") {
		t.Errorf("brief not shown:\n%s", e.out)
	}
	if st, _ := state.Load(); st.Current != "white/a" {
		t.Errorf("current = %q", st.Current)
	}
}

func TestStartUnknownKata(t *testing.T) {
	e := newEnv(t)
	if err := e.run(t, "start", "white/nope"); err == nil {
		t.Fatal("unknown kata should fail")
	}
	if e.lab.has("recreate") {
		t.Error("lab recreated for an unknown kata")
	}
}

func TestCheckFailingDoesNotRecordPass(t *testing.T) {
	e := newEnv(t)
	e.run(t, "start", "white/a")
	e.lab.checkCode = 1
	if err := e.run(t, "check"); !errors.Is(err, ErrCheckFailed) {
		t.Fatalf("err = %v, want ErrCheckFailed", err)
	}
	if !strings.Contains(e.out.String(), "✗ two") {
		t.Errorf("results not rendered:\n%s", e.out)
	}
	if st, _ := state.Load(); len(st.Passed) != 0 {
		t.Errorf("pass recorded on failure: %v", st.Passed)
	}
}

func TestCheckRebootsThenRecordsPass(t *testing.T) {
	e := newEnv(t)
	e.run(t, "start", "white/a")
	if err := e.run(t, "check"); err != nil {
		t.Fatal(err)
	}
	if !e.lab.has("reboot check") {
		t.Errorf("calls = %v, want reboot before check", e.lab.calls)
	}
	if st, _ := state.Load(); st.Passed["white/a"].IsZero() {
		t.Error("pass not recorded")
	}
}

func TestQuickCheckSkipsRebootAndNeverPasses(t *testing.T) {
	e := newEnv(t)
	e.run(t, "start", "white/a")
	if err := e.run(t, "check", "--quick"); err != nil {
		t.Fatal(err)
	}
	if e.lab.has("reboot") {
		t.Error("--quick rebooted")
	}
	if st, _ := state.Load(); len(st.Passed) != 0 {
		t.Error("--quick recorded a pass without the reboot")
	}
}

func TestHintsInOrderThenRunOut(t *testing.T) {
	e := newEnv(t)
	e.run(t, "start", "white/a")
	for _, want := range []string{"Hint 1/2: one", "Hint 2/2: two", "No more hints"} {
		if err := e.run(t, "hint"); err != nil || !strings.Contains(e.out.String(), want) {
			t.Errorf("want %q, got %v:\n%s", want, err, e.out)
		}
	}
	e.run(t, "start", "white/a")
	e.run(t, "hint")
	if !strings.Contains(e.out.String(), "Hint 1/2") {
		t.Error("a fresh start should reset hints")
	}
}

func TestCleanClearsCurrentKeepsHistory(t *testing.T) {
	e := newEnv(t)
	e.run(t, "start", "white/a")
	if err := e.run(t, "check"); err != nil {
		t.Fatal(err)
	}
	e.run(t, "start", "white/a")
	if err := e.run(t, "stop"); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{"status", "hint", "ssh", "check"} {
		if err := e.run(t, cmd); err == nil || !strings.Contains(err.Error(), "no kata in progress") {
			t.Errorf("%s after stop: err = %v", cmd, err)
		}
	}
	if st, _ := state.Load(); st.Current != "" || st.Passed["white/a"].IsZero() {
		t.Errorf("state after clean: %+v", st)
	}
}

func TestBackendNotAvailable(t *testing.T) {
	e := newEnv(t)
	e.lab.notReady = errors.New("lima is not installed")
	if err := e.run(t, "start", "white/a"); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Errorf("err = %v", err)
	}
}

func TestStatusAndSSHHappyPaths(t *testing.T) {
	e := newEnv(t)
	e.run(t, "start", "white/a")
	if err := e.run(t, "status"); err != nil || !strings.Contains(e.out.String(), "Fix A.") || strings.Contains(e.out.String(), "Passed on") {
		t.Errorf("status: %v\n%s", err, e.out)
	}
	if err := e.run(t, "ssh"); err != nil || !e.lab.has("shell") {
		t.Errorf("ssh:%v, calls %v", err, e.lab.calls)
	}
	e.run(t, "check")
	if e.run(t, "status"); !strings.Contains(e.out.String(), "Passed on 2026-11-16") {
		t.Errorf("status after passing:\n%s", e.out)
	}
}

func current(t *testing.T) string {
	t.Helper()
	st, err := state.Load()
	if err != nil {
		t.Fatal(err)
	}
	return st.Current
}

func TestFailedStartLeavesNoActiveKata(t *testing.T) {
	for name, tweak := range map[string]func(*fakeLab){
		"recreate": func(f *fakeLab) { f.recreateErr = errors.New("boom") },
		"setup":    func(f *fakeLab) { f.setupCode = 3 },
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			if err := e.run(t, "start", "white/a"); err != nil {
				t.Fatal(err)
			}
			tweak(e.lab)
			if err := e.run(t, "start", "white/a"); err == nil {
				t.Fatal("start should fail")
			}
			if c := current(t); c != "" {
				t.Errorf("current = %q after a failed start", c)
			}
			if err := e.run(t, "status"); err == nil || !strings.Contains(err.Error(), "no kata in progress") {
				t.Errorf("status err = %v", err)
			}
		})
	}
}

func TestFailedCleanKeepsState(t *testing.T) {
	e := newEnv(t)
	e.run(t, "start", "white/a")
	e.lab.deleteErr = errors.New("busy")
	if err := e.run(t, "stop"); err == nil {
		t.Fatal("clean should fail")
	}
	if c := current(t); c != "white/a" {
		t.Errorf("current = %q, want state untouched", c)
	}
}

func TestStateWriteFailureIsReported(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a read-only directory the process cannot write")
	}
	e := newEnv(t)
	e.run(t, "start", "white/a")
	home := os.Getenv("DOJO_HOME")
	if err := os.Chmod(home, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(home, 0o700) })
	err := e.run(t, "check")
	if err == nil || !strings.Contains(err.Error(), "state.json") {
		t.Fatalf("err = %v, want a state error naming the file", err)
	}
	if strings.Contains(e.out.String(), "Kata passed") {
		t.Errorf("reported success:\n%s", e.out)
	}
}

type recordingFS struct {
	fs.FS
	opened []string
}

func (r *recordingFS) Open(name string) (fs.File, error) {
	r.opened = append(r.opened, name)
	return r.FS.Open(name)
}

func TestNeverOpensSolutions(t *testing.T) {
	e := newEnv(t)
	m := fstest.MapFS{}
	for k, v := range catalog {
		m[k] = v
	}
	m["white/a/solution.sh"] = &fstest.MapFile{Data: []byte("SOLUTION")}
	m["white/a/wrong-x.sh"] = &fstest.MapFile{Data: []byte("WRONG")}
	rec := &recordingFS{FS: m}
	e.app.Catalog = rec
	for _, args := range [][]string{{"start", "white/a"}, {"status"}, {"hint"}, {"check"}} {
		if err := e.run(t, args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	for _, name := range rec.opened {
		if strings.Contains(name, "solution") || strings.Contains(name, "wrong-") {
			t.Errorf("opened %q", name)
		}
	}
}

func TestNegativeHintsShownDoesNotPanic(t *testing.T) {
	e := newEnv(t)
	e.run(t, "start", "white/a")
	st, _ := state.Load()
	st.HintsShown = -5
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	if err := e.run(t, "hint"); err != nil || !strings.Contains(e.out.String(), "Hint 1/2") {
		t.Errorf("hint: %v\n%s", err, e.out)
	}
}

func TestInterruptedCommandsReturnTheContextError(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	e.lab.onRun = cancel
	e.lab.setupCode = -1
	err := e.app.Run(ctx, []string{"start", "white/a"})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("start err = %v", err)
	}
	e = newEnv(t)
	e.run(t, "start", "white/a")
	ctx, cancel = context.WithCancel(context.Background())
	e.lab.onRun = cancel
	e.lab.checkCode = 1
	err = e.app.Run(ctx, []string{"check"})
	if !errors.Is(err, context.Canceled) || strings.Contains(e.out.String(), "Not fixed yet") {
		t.Errorf("check err = %v\n%s", err, e.out)
	}
}

func TestExtraArgsAndHelp(t *testing.T) {
	e := newEnv(t)
	for _, cmd := range []string{"doctor", "status", "hint", "ssh", "check", "stop"} {
		if err := e.run(t, cmd, "extra"); err == nil || !strings.Contains(err.Error(), "takes no arguments") {
			t.Errorf("%s extra: err = %v", cmd, err)
		}
		if err := e.run(t, cmd, "-h"); err != nil || !strings.Contains(e.out.String(), "Usage:") {
			t.Errorf("%s -h: %v\n%s", cmd, err, e.out)
		}
	}
	if err := e.run(t, "start", "white/a", "extra"); err == nil {
		t.Error("start with two args should fail")
	}
	if e.lab.has("recreate") || e.lab.has("delete") {
		t.Errorf("rejected args touched the lab: %v", e.lab.calls)
	}
}

func TestCorruptStateNamesTheFile(t *testing.T) {
	e := newEnv(t)
	home := os.Getenv("DOJO_HOME")
	os.MkdirAll(home, 0o700)
	os.WriteFile(filepath.Join(home, "state.json"), []byte("{nope"), 0o600)
	if err := e.run(t, "status"); err == nil || !strings.Contains(err.Error(), "state.json") || !strings.Contains(err.Error(), "remove it") {
		t.Errorf("err = %v", err)
	}
}

func TestDoctorReady(t *testing.T) {
	e := newEnv(t)
	if err := e.run(t, "doctor"); err != nil {
		t.Fatalf("doctor on a good machine: %v\n%s", err, e.out)
	}
	for _, want := range []string{"✓ 16 GB RAM", "✓ VM manager ready (fake)", "Ready. Next: dojo start"} {
		if !strings.Contains(e.out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, e.out)
		}
	}
	if e.lab.calls != nil {
		t.Errorf("doctor touched the lab: %v", e.lab.calls)
	}
}

func TestDoctorNotReady(t *testing.T) {
	e := newEnv(t)
	e.lab.notReady = errors.New("limactl not found")
	e.app.Probe = func(context.Context) doctor.Machine { m := goodMachine; m.MemoryBytes = 4 << 30; return m }
	if err := e.run(t, "doctor"); !errors.Is(err, ErrNotReady) {
		t.Fatalf("err = %v, want ErrNotReady", err)
	}
	for _, want := range []string{"✗ 4 GB RAM", "-> the labs need at least 8 GB of RAM", "✗ VM manager (fake) not ready: limactl not found", "Not ready yet"} {
		if !strings.Contains(e.out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, e.out)
		}
	}
}

func TestDoctorInterruptedPrintsNoReport(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	e.app.Probe = func(context.Context) doctor.Machine { cancel(); return doctor.Machine{} }
	if err := e.app.Run(ctx, []string{"doctor"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if e.out.Len() != 0 {
		t.Errorf("an interrupted doctor printed a report:\n%s", e.out)
	}
}

func TestDoctorUnknownBackendIsAFinding(t *testing.T) {
	e := newEnv(t)
	e.app.NewBackend = func(name string) (lab.Backend, error) { return nil, errors.New("unknown backend " + name) }
	if err := e.run(t, "doctor"); !errors.Is(err, ErrNotReady) || !strings.Contains(e.out.String(), "unknown backend fake") {
		t.Errorf("err = %v\n%s", err, e.out)
	}
}

// writeKey creates a key pair in the test SSH directory and returns the private path.
func writeKey(t *testing.T, name, pub string) string {
	t.Helper()
	dir := os.Getenv("DOJO_SSH_DIR")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	priv := filepath.Join(dir, name)
	for p, body := range map[string]string{priv: "PRIVATE", priv + ".pub": pub + "\n"} {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return priv
}

func savedKey(t *testing.T) string {
	t.Helper()
	st, err := state.Load()
	if err != nil {
		t.Fatal(err)
	}
	return st.SSHKey
}

func installed(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(os.Getenv("DOJO_SSH_DIR"), "dojo_config"))
	if errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestStartSetsUpSSHAndRemembersTheKey(t *testing.T) {
	e := newEnv(t)
	priv := writeKey(t, "id_ed25519", "ssh-ed25519 AAAA ana@x")
	if err := e.run(t, "start", "white/a"); err != nil {
		t.Fatal(err)
	}
	if !e.lab.has("setup authorize sshconfig") {
		t.Errorf("calls = %v, want setup, then authorize, then sshconfig", e.lab.calls)
	}
	if len(e.lab.authorized) != 1 || string(e.lab.authorized[0]) != "ssh-ed25519 AAAA ana@x" || e.lab.configKeys[0] != priv {
		t.Errorf("lab got keys %q, config for %v; want the learner's key %s", e.lab.authorized, e.lab.configKeys, priv)
	}
	if got := installed(t); !strings.Contains(got, "Host dojo") {
		t.Errorf("dojo_config = %q", got)
	}
	if got := savedKey(t); got != priv {
		t.Errorf("state.SSHKey = %q, want %q", got, priv)
	}
	if !strings.Contains(e.out.String(), "ssh dojo is ready") {
		t.Errorf("output:\n%s", e.out)
	}
}

func TestStartWithoutAKeyStillStartsTheKata(t *testing.T) {
	e := newEnv(t)
	if err := e.run(t, "start", "white/a"); err != nil {
		t.Fatal(err)
	}
	if e.lab.has("authorize") || installed(t) != "" || savedKey(t) != "" {
		t.Errorf("nothing should be set up without a key: calls %v, config %q, key %q", e.lab.calls, installed(t), savedKey(t))
	}
	if current(t) != "white/a" {
		t.Error("the kata should be in progress")
	}
	for _, want := range []string{"Warning: ssh dojo is not set up", "no SSH key found", "ssh-keygen -t ed25519", "dojo ssh"} {
		if !strings.Contains(e.out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, e.out)
		}
	}
}

func TestStartSSHFailuresWriteNothingAndKeepTheKata(t *testing.T) {
	for name, set := range map[string]func(*fakeLab){
		"authorize fails": func(f *fakeLab) { f.authorizeErr = errors.New("lab said no") },
		"sshconfig fails": func(f *fakeLab) { f.configErr = errors.New("lab said no") },
	} {
		e := newEnv(t)
		writeKey(t, "id_ed25519", "ssh-ed25519 AAAA ana@x")
		set(e.lab)
		if err := e.run(t, "start", "white/a"); err != nil {
			t.Fatalf("%s: start failed: %v", name, err)
		}
		if installed(t) != "" || savedKey(t) != "" {
			t.Errorf("%s: config %q and key %q written for a setup that failed", name, installed(t), savedKey(t))
		}
		if !strings.Contains(e.out.String(), "lab said no") || current(t) != "white/a" {
			t.Errorf("%s: warning missing or kata not in progress:\n%s", name, e.out)
		}
	}
}

func TestStartKeepsAPinnedKeyAndFallsBackWithAWarning(t *testing.T) {
	e := newEnv(t)
	writeKey(t, "id_ed25519", "ssh-ed25519 AAAA ana@x")
	other := writeKey(t, "id_work", "ssh-ed25519 BBBB work@x")
	st, _ := state.Load()
	st.SSHKey = other
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	if err := e.run(t, "start", "white/a"); err != nil {
		t.Fatal(err)
	}
	if string(e.lab.authorized[0]) != "ssh-ed25519 BBBB work@x" || savedKey(t) != other {
		t.Errorf("a pinned key must win over the default: %q, saved %q", e.lab.authorized, savedKey(t))
	}
	if err := os.Remove(other + ".pub"); err != nil {
		t.Fatal(err)
	}
	e.lab.authorized = nil
	if err := e.run(t, "start", "white/a"); err != nil {
		t.Fatal(err)
	}
	if len(e.lab.authorized) != 0 || !strings.Contains(e.out.String(), "Warning: ssh dojo is not set up") || !strings.Contains(e.out.String(), "--key") {
		t.Errorf("a pinned key that vanished must warn, not silently switch keys:\n%s", e.out)
	}
}

func TestStartKeyIsRememberedAfterSuccess(t *testing.T) {
	e := newEnv(t)
	writeKey(t, "id_ed25519", "ssh-ed25519 AAAA ana@x")
	e.run(t, "start", "white/a")
	other := writeKey(t, "id_work", "ssh-ed25519 BBBB work@x")
	for i, key := range []string{other, other + ".pub"} {
		e.lab.authorized = nil
		args := []string{"start", "--key", key, "white/a"}
		if i == 1 {
			args = []string{"start", "white/a", "--key", key} // the flag may follow the kata
		}
		if err := e.run(t, args...); err != nil {
			t.Fatal(err)
		}
		if string(e.lab.authorized[0]) != "ssh-ed25519 BBBB work@x" || savedKey(t) != other {
			t.Errorf("--key %s: authorized %q, saved %q", key, e.lab.authorized, savedKey(t))
		}
		if !strings.Contains(installed(t), other) {
			t.Errorf("dojo_config does not point at %s:\n%s", other, installed(t))
		}
	}
}

func TestStartKeyFailuresKeepTheOldKey(t *testing.T) {
	e := newEnv(t)
	first := writeKey(t, "id_ed25519", "ssh-ed25519 AAAA ana@x")
	other := writeKey(t, "id_work", "ssh-ed25519 BBBB work@x")
	e.run(t, "start", "white/a")
	before := installed(t)
	e.lab.calls = nil

	// A key that does not exist stops start before the lab is touched.
	if err := e.run(t, "start", "--key", filepath.Join(filepath.Dir(first), "missing"), "white/a"); err == nil {
		t.Error("a missing key should fail")
	}
	if e.lab.calls != nil {
		t.Errorf("the lab was touched for a key that does not exist: %v", e.lab.calls)
	}
	// A key the lab refuses only warns: the kata still starts, the old key stays.
	e.lab.authorizeErr = errors.New("lab said no")
	if err := e.run(t, "start", "--key", other, "white/a"); err != nil || !strings.Contains(e.out.String(), "lab said no") {
		t.Errorf("err = %v\n%s", err, e.out)
	}
	e.lab.authorizeErr, e.lab.configErr = nil, errors.New("config said no")
	if err := e.run(t, "start", "--key", other, "white/a"); err != nil || !strings.Contains(e.out.String(), "config said no") {
		t.Errorf("err = %v\n%s", err, e.out)
	}
	if savedKey(t) != first || installed(t) != before {
		t.Errorf("failed runs changed things: key %q, config %q", savedKey(t), installed(t))
	}
}

func TestStartArgsAndFlags(t *testing.T) {
	e := newEnv(t)
	for _, args := range [][]string{
		{"start", "white/a", "extra"},
		{"start", "white/a", "--key", "x", "extra"},
		{"start", "--nope", "white/a"},
		{"start", "white/a", "--nope"},
		{"start", "--key"},
		{"start", "--key", "", "white/a"},
		{"start", "white/a", "--key="},
		{"start"},
	} {
		if err := e.run(t, args...); err == nil {
			t.Errorf("%v should fail", args)
		}
	}
	if err := e.run(t, "check", "--key", "x"); err == nil {
		t.Error("--key belongs to start only")
	}
	if e.lab.calls != nil {
		t.Errorf("lab touched: %v", e.lab.calls)
	}
}

func TestRemovedVerbsAreUnknownWithAHint(t *testing.T) {
	for old, want := range map[string]string{
		"shell":      "dojo ssh",
		"clean":      "dojo stop",
		"ssh-config": "dojo start",
		"list":       "dojo start",
	} {
		e := newEnv(t)
		err := e.run(t, old)
		if err == nil || !strings.Contains(err.Error(), "unknown command") || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want unknown command naming %q", old, err, want)
		}
		if e.lab.calls != nil || e.out.Len() != 0 {
			t.Errorf("%s ran something: calls %v, out %q", old, e.lab.calls, e.out)
		}
	}
}

func TestNewVerbsAreInTheHelpAndOldOnesAreNot(t *testing.T) {
	e := newEnv(t)
	if err := e.run(t, "help"); err != nil {
		t.Fatal(err)
	}
	out := e.out.String()
	for _, want := range []string{"dojo start [--key PATH] <kata>", "dojo ssh ", "dojo stop "} {
		if !strings.Contains(out, want) {
			t.Errorf("help misses %q:\n%s", want, out)
		}
	}
	for _, gone := range []string{"dojo list", "dojo shell", "dojo clean", "dojo ssh-config"} {
		if strings.Contains(out, gone) {
			t.Errorf("help still lists %q:\n%s", gone, out)
		}
	}
}

func TestRemovedVerbsAreNeverRecorded(t *testing.T) {
	for old := range removed {
		if commands[old] {
			t.Errorf("%s must not be a recorded command", old)
		}
	}
}

func TestCheckPointsSSHAtTheRebootedLabAndSurvivesFailure(t *testing.T) {
	e := newEnv(t)
	writeKey(t, "id_ed25519", "ssh-ed25519 AAAA ana@x")
	e.run(t, "start", "white/a")
	e.lab.calls = nil
	if err := e.run(t, "check"); err != nil {
		t.Fatal(err)
	}
	if !e.lab.has("reboot authorize sshconfig check") {
		t.Errorf("calls = %v, want ssh refreshed after the reboot, before the check", e.lab.calls)
	}
	if strings.Contains(e.out.String(), "ssh dojo is ready") {
		t.Errorf("a successful refresh after check's reboot must stay quiet:\n%s", e.out)
	}
	e.lab.authorizeErr = errors.New("lab said no")
	if err := e.run(t, "check"); err != nil || !strings.Contains(e.out.String(), "Kata passed") {
		t.Errorf("a failed ssh refresh must not fail the check: %v\n%s", err, e.out)
	}
	if !strings.Contains(e.out.String(), "Warning: ssh dojo is not set up") {
		t.Errorf("a failed refresh must still warn:\n%s", e.out)
	}
	e.lab.calls = nil
	e.run(t, "check", "--quick")
	if e.lab.has("authorize") {
		t.Error("--quick does not reboot, so ssh needs no refresh")
	}
}

func TestInterruptDuringSSHSetupIsReturned(t *testing.T) {
	e := newEnv(t)
	writeKey(t, "id_ed25519", "ssh-ed25519 AAAA ana@x")
	ctx, cancel := context.WithCancel(context.Background())
	e.lab.onAuthorize = cancel
	e.lab.authorizeErr = errors.New("signal: killed")
	err := e.app.Run(ctx, []string{"start", "white/a"})
	if !errors.Is(err, context.Canceled) || strings.Contains(e.out.String(), "Lab ready") {
		t.Errorf("err = %v\n%s", err, e.out)
	}
}

func TestDoctorReportsTheSSHKeyAndSigning(t *testing.T) {
	e := newEnv(t)
	if err := e.run(t, "doctor"); err != nil || !strings.Contains(e.out.String(), "no SSH key found") {
		t.Errorf("no key: %v\n%s", err, e.out)
	}
	if !strings.Contains(e.out.String(), "-> create an SSH key first, then:\n         git config --global gpg.format ssh") {
		t.Errorf("multi-line advice should be indented under its item:\n%s", e.out)
	}
	priv := writeKey(t, "id_ed25519", "ssh-ed25519 AAAA ana@x")
	e.app.Probe = func(context.Context) doctor.Machine {
		m := goodMachine
		m.HasSSHKeygen = true
		m.GitSigningFormat, m.GitSigningKey, m.GitSignCommits = "ssh", priv+".pub", "true"
		return m
	}
	if err := e.run(t, "doctor"); err != nil || !strings.Contains(e.out.String(), "SSH key found: id_ed25519") || !strings.Contains(e.out.String(), "configured to sign commits with id_ed25519") {
		t.Errorf("key and signing set: %v\n%s", err, e.out)
	}
	other := writeKey(t, "id_work", "ssh-ed25519 BBBB work@x")
	st, _ := state.Load()
	st.SSHKey = other
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	if err := e.run(t, "doctor"); err != nil || !strings.Contains(e.out.String(), "not with the key dojo uses (id_work)") {
		t.Errorf("doctor must judge the key dojo uses (the pinned one): %v\n%s", err, e.out)
	}
}

func TestCleanClearsSSHDojoAfterTheLabIsGone(t *testing.T) {
	e := newEnv(t)
	writeKey(t, "id_ed25519", "ssh-ed25519 AAAA ana@x")
	e.run(t, "start", "white/a")
	if installed(t) == "" {
		t.Fatal("setup: start should have written the ssh dojo entry")
	}
	cfgPath := filepath.Join(os.Getenv("DOJO_SSH_DIR"), "config")
	cfgBefore, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.run(t, "stop"); err != nil {
		t.Fatal(err)
	}
	if got := installed(t); got != "" {
		t.Errorf("dojo_config after clean = %q, want empty (no dead port)", got)
	}
	cfgAfter, err := os.ReadFile(cfgPath)
	if err != nil || string(cfgAfter) != string(cfgBefore) {
		t.Errorf("the learner's config must stay as is: %v\n%s", err, cfgAfter)
	}
	if c := current(t); c != "" {
		t.Errorf("current = %q, want cleared", c)
	}
}

func TestCleanWithoutSSHSetupSucceedsAndCreatesNothing(t *testing.T) {
	e := newEnv(t)
	e.run(t, "start", "white/a")
	if err := e.run(t, "stop"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("DOJO_SSH_DIR"), "dojo_config")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("clean must not create dojo_config: %v", err)
	}
}

func TestFailedCleanKeepsTheSSHEntry(t *testing.T) {
	e := newEnv(t)
	writeKey(t, "id_ed25519", "ssh-ed25519 AAAA ana@x")
	e.run(t, "start", "white/a")
	before := installed(t)
	e.lab.deleteErr = errors.New("busy")
	if err := e.run(t, "stop"); err == nil {
		t.Fatal("clean should fail")
	}
	if got := installed(t); got == "" || got != before {
		t.Errorf("the lab may still exist, so ssh dojo must stay: %q", got)
	}
}

func TestCleanReportsAnSSHRemovalFailureAfterDeletingTheLab(t *testing.T) {
	e := newEnv(t)
	writeKey(t, "id_ed25519", "ssh-ed25519 AAAA ana@x")
	e.run(t, "start", "white/a")
	// A non-empty directory in the file's place makes the atomic rewrite fail.
	path := filepath.Join(os.Getenv("DOJO_SSH_DIR"), "dojo_config")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(path, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	e.lab.calls = nil
	err := e.run(t, "stop")
	if err == nil || !strings.Contains(err.Error(), "ssh dojo could not be cleared") {
		t.Fatalf("err = %v, want the ssh failure named", err)
	}
	if !e.lab.has("delete") {
		t.Errorf("calls = %v, want the lab deleted", e.lab.calls)
	}
	if c := current(t); c != "" {
		t.Errorf("current = %q: the lab is gone, so state must say so", c)
	}
}

func TestCheckKeepsItsOutputForTheMentor(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		code  int
		wantE error
	}{
		{"failing", []string{"check"}, 1, ErrCheckFailed},
		{"passing", []string{"check"}, 0, nil},
		{"quick failing", []string{"check", "--quick"}, 1, ErrCheckFailed},
		{"quick passing", []string{"check", "--quick"}, 0, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.run(t, "start", "white/a")
			e.lab.checkCode = tc.code
			e.lab.checkOut = []byte("PASS  one\nFAIL  two: mode is 666\n")
			if err := e.run(t, tc.args...); !errors.Is(err, tc.wantE) {
				t.Fatalf("err = %v, want %v", err, tc.wantE)
			}
			if st, _ := state.Load(); st.LastCheck != "PASS  one\nFAIL  two: mode is 666\n" {
				t.Errorf("LastCheck = %q", st.LastCheck)
			}
		})
	}
}

func TestCheckThatCouldNotRunKeepsThePreviousOutput(t *testing.T) {
	e := newEnv(t)
	e.run(t, "start", "white/a")
	e.lab.checkOut = []byte("first run")
	e.lab.checkCode = 1
	e.run(t, "check", "--quick")
	e.lab.checkErr = errors.New("ssh broke")
	if err := e.run(t, "check", "--quick"); err == nil {
		t.Fatal("a check that could not run should fail")
	}
	if st, _ := state.Load(); st.LastCheck != "first run" {
		t.Errorf("LastCheck = %q, want the previous run kept", st.LastCheck)
	}
}

func TestStartAndCleanDropTheLastCheck(t *testing.T) {
	e := newEnv(t)
	e.run(t, "start", "white/a")
	e.lab.checkOut = []byte("old run")
	e.lab.checkCode = 1
	e.run(t, "check", "--quick")
	e.run(t, "stop")
	if st, _ := state.Load(); st.LastCheck != "" {
		t.Errorf("clean kept LastCheck = %q", st.LastCheck)
	}
	e.run(t, "start", "white/a")
	e.run(t, "check", "--quick")
	e.lab.recreateErr = errors.New("boom")
	e.run(t, "start", "white/a")
	if st, _ := state.Load(); st.LastCheck != "" {
		t.Errorf("start kept LastCheck = %q", st.LastCheck)
	}
}

func TestStartWritesTheMentorFilesAfterTheLabIsReady(t *testing.T) {
	e := newEnv(t)
	if err := e.run(t, "start", "white/a"); err != nil {
		t.Fatal(err)
	}
	if !e.lab.has("setup mentor") {
		t.Errorf("calls = %v, want the mentor files after setup", e.lab.calls)
	}
	if len(e.lab.mentorScripts) != 1 {
		t.Fatalf("mentor scripts = %d, want 1", len(e.lab.mentorScripts))
	}
	script := string(e.lab.mentorScripts[0])
	for _, want := range []string{"Never write the fix", "AGENTS.md", "@AGENTS.md", "chown"} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q", want)
		}
	}
	for _, leak := range []string{"SETUP", "CHECK"} {
		if strings.Contains(script, leak) {
			t.Errorf("script carries kata content %q", leak)
		}
	}
}

func TestMentorFileFailuresWarnAndKeepTheKata(t *testing.T) {
	for name, set := range map[string]func(*fakeLab){
		"exit code": func(f *fakeLab) { f.mentorCode = 1 },
		"run error": func(f *fakeLab) { f.mentorErr = errors.New("vm gone") },
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			set(e.lab)
			if err := e.run(t, "start", "white/a"); err != nil {
				t.Fatalf("start failed: %v", err)
			}
			if !strings.Contains(e.out.String(), "Warning: the mentor rules") || !strings.Contains(e.out.String(), "Lab ready") {
				t.Errorf("want a warning and a ready lab:\n%s", e.out)
			}
			if got := current(t); got != "white/a" {
				t.Errorf("current = %q", got)
			}
		})
	}
}

func TestMentorFilesNotWrittenWhenSetupFails(t *testing.T) {
	e := newEnv(t)
	e.lab.setupCode = 1
	if err := e.run(t, "start", "white/a"); err == nil {
		t.Fatal("setup failure should fail start")
	}
	if len(e.lab.mentorScripts) != 0 {
		t.Error("mentor files written into a lab whose setup failed")
	}
}
