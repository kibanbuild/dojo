//go:build !windows

// Lima does not run on Windows hosts, and these fakes are shell scripts.

package lab

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

func TestLimaAuthorizeKeyPipesScriptToShell(t *testing.T) {
	log := filepath.Join(t.TempDir(), "stdin")
	l := fakeLima(t, "echo \"$*\" > "+log+".args\ncat > "+log+"\n")
	if err := l.AuthorizeKey(context.Background(), []byte("ssh-ed25519 AAAA me")); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(log + ".args")
	if strings.TrimSpace(string(args)) != "shell --workdir / dojo bash -s" {
		t.Errorf("args = %q", args)
	}
	script, _ := os.ReadFile(log)
	if !strings.Contains(string(script), "authorized_keys") {
		t.Errorf("script = %q", script)
	}
	if err := l.AuthorizeKey(context.Background(), []byte("nope")); err == nil {
		t.Error("an invalid key must fail before limactl runs")
	}
	if err := fakeLima(t, "echo denied >&2\nexit 1\n").AuthorizeKey(context.Background(), []byte("ssh-ed25519 AAAA")); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Errorf("error should carry output, got %v", err)
	}
}

func TestLimaSSHConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ssh.config"), []byte("Host lima-dojo\n  Port 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	l := fakeLima(t, "echo "+dir+"\n")
	got, err := l.SSHConfig(context.Background(), "/k/id")
	if err != nil || !strings.Contains(got, "Host dojo\n") || !strings.Contains(got, `IdentityFile "/k/id"`) {
		t.Fatalf("SSHConfig = %q, %v", got, err)
	}
	for _, bad := range []string{"/k/\"id", "/k/%d/id", "/k/${HOME}/id", "/k/a\nb"} {
		if _, err := l.SSHConfig(context.Background(), bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
	if _, err := fakeLima(t, "echo "+t.TempDir()+"\n").SSHConfig(context.Background(), "/k/id"); err == nil {
		t.Error("a missing ssh.config must fail")
	}
}
