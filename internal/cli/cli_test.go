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
}

func (f *fakeLab) Name() string      { return "fake" }
func (f *fakeLab) Available() error  { return f.notReady }
func (f *fakeLab) record(c string)   { f.calls = append(f.calls, c) }
func (f *fakeLab) has(c string) bool { return strings.Contains(strings.Join(f.calls, " "), c) }

func (f *fakeLab) Recreate(context.Context) error       { f.record("recreate"); return f.recreateErr }
func (f *fakeLab) Exists(context.Context) (bool, error) { return true, nil }
func (f *fakeLab) Reboot(context.Context) error         { f.record("reboot"); return nil }
func (f *fakeLab) Shell(context.Context) error          { f.record("shell"); return nil }
func (f *fakeLab) Delete(context.Context) error         { f.record("delete"); return f.deleteErr }
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
		if f.checkCode != 0 {
			return []byte("PASS  one\nFAIL  two\n"), f.checkCode, nil
		}
		return []byte("PASS  one\nPASS  two\n"), 0, nil
	}
	return nil, 99, errors.New("unexpected script")
}

var catalog = fstest.MapFS{
	"white/a/kata.yaml": {Data: []byte("id: white/a\ntitle: Kata A\nbelt: white\nsummary: first\nbrief: Fix A.\nhints: [one, two]\n")},
	"white/a/setup.sh":  {Data: []byte("SETUP")},
	"white/a/check.sh":  {Data: []byte("CHECK")},
}

type env struct {
	app *App
	lab *fakeLab
	out *bytes.Buffer
}

func newEnv(t *testing.T) *env {
	t.Helper()
	t.Setenv("DOJO_HOME", filepath.Join(t.TempDir(), "home"))
	f := &fakeLab{}
	out := &bytes.Buffer{}
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	app := &App{
		Out: out, Catalog: catalog, BackendName: "fake",
		NewBackend: func(string) (lab.Backend, error) { return f, nil },
		Now:        func() time.Time { return now },
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
	for _, cmd := range []string{"status", "hint", "shell", "check"} {
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
	e.run(t, "list")
	if !strings.Contains(e.out.String(), "✓ white/a") {
		t.Errorf("list does not mark the pass:\n%s", e.out)
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
	if err := e.run(t, "clean"); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{"status", "hint", "shell", "check"} {
		if err := e.run(t, cmd); err == nil || !strings.Contains(err.Error(), "no kata in progress") {
			t.Errorf("%s after clean: err = %v", cmd, err)
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

func TestListStatusAndShellHappyPaths(t *testing.T) {
	e := newEnv(t)
	if err := e.run(t, "list"); err != nil || !strings.Contains(e.out.String(), "  white/a") {
		t.Errorf("list before passing: %v\n%s", err, e.out)
	}
	e.run(t, "start", "white/a")
	if err := e.run(t, "status"); err != nil || !strings.Contains(e.out.String(), "Fix A.") || strings.Contains(e.out.String(), "Passed on") {
		t.Errorf("status: %v\n%s", err, e.out)
	}
	if err := e.run(t, "shell"); err != nil || !e.lab.has("shell") {
		t.Errorf("shell: %v, calls %v", err, e.lab.calls)
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
	if err := e.run(t, "clean"); err == nil {
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
	for _, args := range [][]string{{"list"}, {"start", "white/a"}, {"status"}, {"hint"}, {"check"}} {
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
	for _, cmd := range []string{"list", "status", "hint", "shell", "check", "clean"} {
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
	if err := e.run(t, "list"); err == nil || !strings.Contains(err.Error(), "state.json") || !strings.Contains(err.Error(), "remove it") {
		t.Errorf("err = %v", err)
	}
}
