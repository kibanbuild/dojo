//go:build !windows

package doctor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// fakeGit puts a git on PATH that prints canned answers, so the probe's
// arguments and parsing are tested without the machine's own git config.
func fakeGit(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\n"+script), 0o755); err != nil { //nolint:gosec // test helper must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func TestProbeGitReadsVersionIdentityAndSigning(t *testing.T) {
	fakeGit(t, `case "$1" in
--version) echo "git version 2.39.5 (Apple Git-154)";;
config) case "$*" in
  *--global*--includes*--get-regexp*) printf 'user.name Ana\nuser.email ana@x.dev\ngpg.format ssh\nuser.signingkey ~/.ssh/id_ed25519.pub\ncommit.gpgsign true\n';;
  *) exit 7;;
esac;;
esac`)
	var m Machine
	probeGit(context.Background(), &m)
	if !m.HasGit || m.GitMajor != 2 || m.GitMinor != 39 || m.GitName != "Ana" || m.GitEmail != "ana@x.dev" ||
		m.GitSigningFormat != "ssh" || m.GitSigningKey != "~/.ssh/id_ed25519.pub" || m.GitSignCommits != "true" {
		t.Errorf("probe = %+v", m)
	}
}

func TestProbeGitWithNoGlobalConfigStillHasGit(t *testing.T) {
	fakeGit(t, `case "$1" in
--version) echo "git version 2.45.1";;
*) exit 1;;
esac`)
	var m Machine
	probeGit(context.Background(), &m)
	if !m.HasGit || m.GitMajor != 2 || m.GitName != "" || m.GitSigningKey != "" {
		t.Errorf("probe = %+v", m)
	}
}
