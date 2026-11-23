package doctor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kibanbuild/dojo/internal/sshconfig"
)

// ProbeSSH finds the SSH key dojo would use (chosen, or the default in the SSH
// directory) and whether git's signing key is that key. It reads public key
// files only.
func ProbeSSH(m *Machine, chosen string) {
	dir, err := sshconfig.Dir()
	if err != nil {
		m.SSHKeyErr = err
		return
	}
	priv, pub, err := sshconfig.ResolveKey(dir, chosen)
	if err != nil {
		m.SSHKeyErr = err
		return
	}
	m.SSHKeyPath = priv
	m.SigningKeyIsSSHKey = signingKeyIs(m.GitSigningKey, priv, pub)
}

// signingKeyIs reports whether git's user.signingkey names the key: a literal
// public key (optionally "key::"-prefixed) or a path to the .pub or private file.
func signingKeyIs(value, priv string, pub []byte) bool {
	value = strings.TrimPrefix(strings.TrimSpace(value), "key::")
	if value == "" {
		return false
	}
	if strings.HasPrefix(value, "ssh-") || strings.HasPrefix(value, "ecdsa-") || strings.HasPrefix(value, "sk-") {
		a, b := strings.Fields(value), strings.Fields(string(pub))
		return len(a) >= 2 && len(b) >= 2 && a[0] == b[0] && a[1] == b[1]
	}
	if value == "~" || strings.HasPrefix(value, "~/") || strings.HasPrefix(value, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		value = filepath.Join(home, value[1:])
	}
	return sameFile(value, priv+".pub") || sameFile(value, priv)
}

func sameFile(a, b string) bool {
	sa, err := os.Stat(a)
	if err != nil {
		return false
	}
	sb, err := os.Stat(b)
	return err == nil && os.SameFile(sa, sb)
}

// sshChecks covers the key, ssh-keygen and git commit signing. It only states
// what was measured: config values and file existence, never that GitHub knows the key.
func sshChecks(m Machine) []Result {
	var rs []Result
	keygen := `ssh-keygen -t ed25519 -C "you@example.com"`
	switch {
	case m.SSHKeyPath != "":
		rs = append(rs, Result{OK, "SSH key found: " + filepath.Base(m.SSHKeyPath), ""})
	case errors.Is(m.SSHKeyErr, sshconfig.ErrNoKey):
		rs = append(rs, Result{Warn, "no SSH key found (needed for ssh dojo, GitHub and signed commits)", keygen})
	default:
		rs = append(rs, Result{Warn, fmt.Sprintf("could not use an SSH key: %v", m.SSHKeyErr),
			"fix or replace it, or pick another with dojo start --key <path> <kata>"})
	}
	if !m.HasSSHKeygen {
		rs = append(rs, Result{Warn, "`ssh-keygen` not found (git needs it to sign commits)", keygenAdvice(m.OS)})
	}
	if !m.HasGit {
		return rs
	}
	pubArg := "~/.ssh/id_ed25519.pub"
	if m.SSHKeyPath != "" {
		pubArg = m.SSHKeyPath + ".pub"
	}
	setup := "git config --global gpg.format ssh\n" +
		"git config --global user.signingkey " + quote(pubArg) + "\n" +
		"git config --global commit.gpgsign true\n" +
		"then add the same .pub on GitHub: Settings > SSH and GPG keys > New SSH key > Key type: Signing Key"
	if m.SSHKeyPath == "" {
		setup = "create an SSH key first, then:\n" + setup
	}
	configured := m.GitSigningFormat == "ssh" && m.GitSigningKey != "" && m.GitSignCommits == "true"
	switch {
	case !configured:
		rs = append(rs, Result{Warn, "git is not set up to sign commits with an SSH key", setup})
	case m.GitMajor != 0 && (m.GitMajor < 2 || m.GitMajor == 2 && m.GitMinor < 34):
		rs = append(rs, Result{Warn, fmt.Sprintf("git %d.%d is too old to sign with an SSH key (2.34 or newer)", m.GitMajor, m.GitMinor), gitUpdateAdvice(m.OS)})
	case m.SSHKeyPath == "":
		rs = append(rs, Result{OK, "git is configured to sign commits with an SSH key", ""})
	case m.SigningKeyIsSSHKey:
		rs = append(rs, Result{OK, "git is configured to sign commits with " + filepath.Base(m.SSHKeyPath), ""})
	default:
		rs = append(rs, Result{Warn, fmt.Sprintf("git signs commits with %s, not with the key dojo uses (%s)", m.GitSigningKey, filepath.Base(m.SSHKeyPath)),
			"git config --global user.signingkey " + quote(pubArg) + ", or ignore this if that other key is on purpose"})
	}
	return rs
}

func keygenAdvice(goos string) string {
	switch goos {
	case "windows":
		return "install the OpenSSH Client (Settings > System > Optional features) or Git for Windows"
	case "darwin":
		return "ssh-keygen ships with macOS; check that /usr/bin is on your PATH"
	default:
		return "install the OpenSSH client (Debian/Ubuntu: sudo apt install openssh-client; Fedora: sudo dnf install openssh-clients)"
	}
}

func gitUpdateAdvice(goos string) string {
	switch goos {
	case "windows":
		return "update git: https://git-scm.com/downloads"
	case "darwin":
		return "update git: brew install git"
	default:
		return "update git with your package manager, or from https://git-scm.com/downloads"
	}
}

// quote wraps a path in double quotes, which sh, PowerShell and cmd all accept;
// %q would double the backslashes of a Windows path.
func quote(p string) string { return `"` + p + `"` }
