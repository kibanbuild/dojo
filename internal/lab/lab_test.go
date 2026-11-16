package lab

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNew(t *testing.T) {
	b, err := New(DefaultName())
	if err != nil || b.Name() != "lima" {
		t.Fatalf("default backend = %v, %v", b, err)
	}
	_, err = New("nope")
	if err == nil || !strings.Contains(err.Error(), `"nope"`) || !strings.Contains(err.Error(), "known: lima") {
		t.Fatalf("unknown backend error = %v", err)
	}
}

func TestLastLines(t *testing.T) {
	for _, tc := range []struct {
		in   string
		n    int
		want string
	}{
		{"a\nb\nc\n", 2, "b\nc"},
		{"a\nb", 5, "a\nb"},
		{"", 3, ""},
	} {
		if got := lastLines(tc.in, tc.n); got != tc.want {
			t.Errorf("lastLines(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}

func fakeLima(t *testing.T, script string) *Lima {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "limactl")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+script), 0o755); err != nil { //nolint:gosec // test script must be executable
		t.Fatal(err)
	}
	return &Lima{Bin: bin}
}

func TestLimaAvailable(t *testing.T) {
	if err := (&Lima{Bin: filepath.Join(t.TempDir(), "missing")}).Available(); err == nil {
		t.Fatal("want an error for a missing binary")
	}
	if err := fakeLima(t, "").Available(); err != nil {
		t.Fatal(err)
	}
}

func TestLimaExists(t *testing.T) {
	ctx := context.Background()
	got, err := fakeLima(t, "echo other\necho dojo\n").Exists(ctx)
	if err != nil || !got {
		t.Fatalf("Exists = %v, %v; want true", got, err)
	}
	got, err = fakeLima(t, "echo other\n").Exists(ctx)
	if err != nil || got {
		t.Fatalf("Exists = %v, %v; want false", got, err)
	}
	if _, err = fakeLima(t, "echo boom >&2\nexit 1\n").Exists(ctx); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error should carry stderr, got %v", err)
	}
}

func TestLimaRunExitCode(t *testing.T) {
	out, code, err := fakeLima(t, "cat >/dev/null\necho hi\nexit 3\n").Run(context.Background(), []byte("true"))
	if err != nil || code != 3 || strings.TrimSpace(string(out)) != "hi" {
		t.Fatalf("Run = %q, %d, %v", out, code, err)
	}
}

// recordingLima logs each limactl invocation's arguments, one line per call.
func recordingLima(t *testing.T, list string) (*Lima, string) {
	t.Helper()
	log := filepath.Join(t.TempDir(), "calls")
	l := fakeLima(t, "echo \"$*\" >> "+log+"\n[ \"$1\" = list ] && echo "+list+"\nexit 0\n")
	return l, log
}

func calls(t *testing.T, log string) []string {
	t.Helper()
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("calls = %q, want %q", got, want)
	}
}

func TestLimaRecreate(t *testing.T) {
	start := "start --tty=false --name=dojo --plain --cpus=2 --memory=2 --disk=10 template:ubuntu-lts"
	l, log := recordingLima(t, "other")
	if err := l.Recreate(context.Background()); err != nil {
		t.Fatal(err)
	}
	eq(t, calls(t, log), []string{"list --format {{.Name}}", start})

	l, log = recordingLima(t, "dojo")
	if err := l.Recreate(context.Background()); err != nil {
		t.Fatal(err)
	}
	eq(t, calls(t, log), []string{"list --format {{.Name}}", "delete --force dojo", start})
}

func TestLimaReboot(t *testing.T) {
	l, log := recordingLima(t, "dojo")
	if err := l.Reboot(context.Background()); err != nil {
		t.Fatal(err)
	}
	eq(t, calls(t, log), []string{"stop dojo", "start --tty=false dojo"})
}

func TestLimaDelete(t *testing.T) {
	l, log := recordingLima(t, "dojo")
	if err := l.Delete(context.Background()); err != nil {
		t.Fatal(err)
	}
	eq(t, calls(t, log), []string{"list --format {{.Name}}", "delete --force dojo"})

	l, log = recordingLima(t, "other")
	if err := l.Delete(context.Background()); err != nil {
		t.Fatal(err)
	}
	eq(t, calls(t, log), []string{"list --format {{.Name}}"})
}

func TestLimaRunStartError(t *testing.T) {
	l := &Lima{Bin: filepath.Join(t.TempDir(), "missing")}
	_, code, err := l.Run(context.Background(), []byte("true"))
	if err == nil || code != -1 {
		t.Fatalf("Run = %d, %v; want -1 and an error", code, err)
	}
}
