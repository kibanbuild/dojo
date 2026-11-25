//go:build !windows

package doctor

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

// probeEnv gives the probe a temp HOME and a PATH holding only the stub commands the test writes.
type probeEnv struct {
	t         *testing.T
	home, bin string
}

func newProbeEnv(t *testing.T) *probeEnv {
	t.Helper()
	e := &probeEnv{t: t, home: t.TempDir(), bin: t.TempDir()}
	t.Setenv("HOME", e.home)
	t.Setenv("PATH", e.bin)
	return e
}

func (e *probeEnv) script(name, body string) {
	e.t.Helper()
	if err := os.WriteFile(filepath.Join(e.bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		e.t.Fatal(err)
	}
}

func (e *probeEnv) write(rel, content string) string {
	e.t.Helper()
	p := filepath.Join(e.home, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		e.t.Fatal(err)
	}
	return p
}

func (e *probeEnv) clone(origin string) {
	e.t.Helper()
	if err := os.MkdirAll(filepath.Join(e.home, "dojo", ".git"), 0o700); err != nil {
		e.t.Fatal(err)
	}
	e.script("git", `echo "`+origin+`"`)
}

func TestProbeGitHubCloneOrigin(t *testing.T) {
	tests := []struct {
		name, origin, base string
		want               bool
	}{
		{"clone over SSH", "git@github.com:ada/dojo.git", "", true},
		{"clone over ssh://", "ssh://git@github.com/ada/dojo.git", "", true},
		{"clone over HTTPS", "https://github.com/ada/dojo.git", "", false},
		{"clone origin wins over the base", "https://github.com/ada/dojo.git", "git@github.com:", false},
		{"no clone, default base is SSH", "", "", true},
		{"no clone, https base", "", "https://github.com/", false},
		{"no clone, file base", "", "file:///srv/git/", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newProbeEnv(t)
			e.script("ssh", "exit 1")
			if tt.origin != "" {
				e.clone(tt.origin)
			}
			var m Machine
			ProbeGitHub(context.Background(), &m, tt.base)
			if m.CloneOverSSH != tt.want {
				t.Errorf("CloneOverSSH = %v", m.CloneOverSSH)
			}
			if !tt.want && m.SSHOfferedKeys != nil {
				t.Errorf("an HTTPS clone must not read keys: %v", m.SSHOfferedKeys)
			}
		})
	}
}

func TestProbeGitHubDirWithoutGitFallsBackToTheBase(t *testing.T) {
	e := newProbeEnv(t)
	e.script("ssh", "exit 1")
	e.script("git", `echo "https://github.com/ada/dojo.git"`)
	if err := os.MkdirAll(filepath.Join(e.home, "dojo"), 0o700); err != nil {
		t.Fatal(err)
	}
	var m Machine
	ProbeGitHub(context.Background(), &m, "")
	if !m.CloneOverSSH {
		t.Error("a folder that is not a clone says nothing about the origin")
	}
}

func TestProbeGitHubOfferedKeys(t *testing.T) {
	e := newProbeEnv(t)
	e.write(".ssh/id_ed25519.pub", pubA+"\n")
	e.write(".ssh/id_work.pub", "not a key\n")
	abs := e.write("keys/abs.pub", pubB+"\n")
	e.script("ssh", `echo "identityfile ~/.ssh/id_ed25519"
echo "identityfile ~/.ssh/id_rsa"
echo "identityfile ~/.ssh/id_work"
echo "identityfile none"
echo "identityfile relative/key"
echo "identityfile `+strings.TrimSuffix(abs, ".pub")+`"
echo "port 22"`)
	e.script("ssh-add", `echo "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAGENT agent"
echo "The agent has no identities."`)
	var m Machine
	ProbeGitHub(context.Background(), &m, "")
	want := []string{pubA, pubB, "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAGENT agent"}
	if !m.CloneOverSSH || !reflect.DeepEqual(m.SSHOfferedKeys, want) {
		t.Errorf("keys = %q", m.SSHOfferedKeys)
	}
}

func TestProbeGitHubNoAgentAndNoSSH(t *testing.T) {
	e := newProbeEnv(t)
	e.write(".ssh/id_ed25519.pub", pubA+"\n")
	e.script("ssh", `echo "identityfile ~/.ssh/id_ed25519"`)
	e.script("ssh-add", `echo "Could not open a connection to your authentication agent." >&2
exit 2`)
	var m Machine
	ProbeGitHub(context.Background(), &m, "")
	if !reflect.DeepEqual(m.SSHOfferedKeys, []string{pubA}) {
		t.Errorf("no agent: keys = %q", m.SSHOfferedKeys)
	}

	t.Setenv("PATH", t.TempDir()) // neither ssh nor ssh-add
	m = Machine{}
	ProbeGitHub(context.Background(), &m, "")
	if !m.CloneOverSSH || len(m.SSHOfferedKeys) != 0 {
		t.Errorf("no ssh: %+v", m)
	}
}

func TestReadPubOnlyReadsSmallRegularKeyFiles(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.pub")
	_ = os.WriteFile(good, []byte(pubA+"\n"), 0o600)
	big := filepath.Join(dir, "big.pub")
	_ = os.WriteFile(big, []byte(pubA+strings.Repeat(" x", 5000)), 0o600)
	fifo := filepath.Join(dir, "fifo.pub")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok := readPub(good); !ok || got != pubA {
		t.Errorf("good = %q %v", got, ok)
	}
	for name, p := range map[string]string{"big": big, "fifo": fifo, "missing": filepath.Join(dir, "none"), "dir": dir} {
		if _, ok := readPub(p); ok {
			t.Errorf("%s was read", name)
		}
	}
}

func TestSigningPubReadsTheKeyGitNames(t *testing.T) {
	e := newProbeEnv(t)
	e.write(".ssh/id.pub", pubB+"\n")
	e.write(".ssh/id", "-----BEGIN OPENSSH PRIVATE KEY-----\n")
	e.write(".ssh/notes", "hello\n")
	for name, tt := range map[string]struct{ value, want string }{
		"tilde":                    {"~/.ssh/id.pub", pubB},
		"private path uses .pub":   {"~/.ssh/id", pubB},
		"absolute":                 {filepath.Join(e.home, ".ssh/id.pub"), pubB},
		"a file that is no key":    {"~/.ssh/notes", ""},
		"missing":                  {"~/.ssh/gone", ""},
		"unset takes the dojo key": {"", pubA},
	} {
		if got := signingPub(tt.value, pubA); got != tt.want {
			t.Errorf("%s: %q, want %q", name, got, tt.want)
		}
	}
}
