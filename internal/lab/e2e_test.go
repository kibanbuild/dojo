//go:build e2e

package lab_test

import (
	"context"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/kibanbuild/dojo/internal/kata"
	"github.com/kibanbuild/dojo/internal/lab"
)

// katasDir is DOJO_KATAS, or the repo's katas/ directory. Solutions and wrong-*.sh are
// not embedded in the binary, so the test reads the catalog from disk.
func katasDir(t *testing.T) string {
	t.Helper()
	if dir := os.Getenv("DOJO_KATAS"); dir != "" {
		return dir
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test file")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "katas")
}

// TestKatasEndToEnd proves every kata on a real VM: check fails after setup,
// and passes after the reference solution and a reboot.
func TestKatasEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	backend, err := lab.New(lab.DefaultName())
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.Available(); err != nil {
		t.Fatal(err)
	}
	catalog := os.DirFS(katasDir(t))
	katas, err := kata.List(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if len(katas) == 0 {
		t.Fatal("the catalog has no katas")
	}
	t.Cleanup(func() { _ = backend.Delete(context.Background()) })

	for _, k := range katas {
		t.Run(k.ID, func(t *testing.T) {
			solution, err := fs.ReadFile(catalog, path.Join(k.ID, "solution.sh"))
			if err != nil {
				t.Fatalf("every kata needs a solution.sh for this test: %v", err)
			}
			if err := backend.Recreate(ctx); err != nil {
				t.Fatal(err)
			}
			mustRun(t, ctx, backend, "setup", k.Setup, 0)
			mustFail(t, ctx, backend, "check on a broken lab", k.Check)
			mustRun(t, ctx, backend, "solution", solution, 0)
			if err := backend.Reboot(ctx); err != nil {
				t.Fatal(err)
			}
			mustRun(t, ctx, backend, "check", k.Check, 0)

			// Every wrong-*.sh is a plausible fix that must not pass.
			wrongs, _ := fs.Glob(catalog, path.Join(k.ID, "wrong-*.sh"))
			for _, w := range wrongs {
				t.Run(path.Base(w), func(t *testing.T) {
					wrong, err := fs.ReadFile(catalog, w)
					if err != nil {
						t.Fatal(err)
					}
					if err := backend.Recreate(ctx); err != nil {
						t.Fatal(err)
					}
					mustRun(t, ctx, backend, "setup", k.Setup, 0)
					mustRun(t, ctx, backend, "wrong fix", wrong, 0)
					if err := backend.Reboot(ctx); err != nil {
						t.Fatal(err)
					}
					mustFail(t, ctx, backend, "check after a wrong fix", k.Check)
				})
			}
		})
	}
}

func mustRun(t *testing.T, ctx context.Context, b lab.Backend, what string, script []byte, want int) {
	t.Helper()
	out, code, err := b.Run(ctx, script)
	if err != nil || code != want {
		t.Fatalf("%s: exit %d (want %d), err %v\n%s", what, code, want, err, out)
	}
	t.Logf("%s ok:\n%s", what, out)
}

// mustFail requires a live VM and a check that ran and exited non-zero, so a dead VM
// (exit -1 or ssh's 255) cannot pass as a failing check.
func mustFail(t *testing.T, ctx context.Context, b lab.Backend, what string, check []byte) {
	t.Helper()
	if _, code, err := b.Run(ctx, []byte("true")); err != nil || code != 0 {
		t.Fatalf("%s: the VM is not alive (exit %d, err %v)", what, code, err)
	}
	out, code, err := b.Run(ctx, check)
	if err != nil || code == -1 || code == 255 {
		t.Fatalf("%s: the check did not run (exit %d, err %v)\n%s", what, code, err, out)
	}
	if code == 0 {
		t.Fatalf("%s: the check passes, it must fail\n%s", what, out)
	}
}
