// Package sshconfig makes the lab reachable as `ssh dojo` by keeping a
// dojo-owned file next to the learner's SSH config and including it from there.
// It reads public keys only; private key files are never opened.
package sshconfig

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// FileName is the dojo-owned file inside the SSH directory.
const FileName = "dojo_config"

// BackupName is a copy of the learner's config taken before dojo first edits it.
const BackupName = "config.before-dojo"

var bom = []byte("\xef\xbb\xbf")

const includeLine = "Include " + FileName

// ErrNoKey means the learner has not created an SSH key yet.
var ErrNoKey = errors.New("no SSH key found")

// KeyNames are the default OpenSSH key files, in order of preference.
var KeyNames = []string{"id_ed25519", "id_ecdsa", "id_rsa"}

// keyPrefixes are the OpenSSH public key types, including hardware security keys.
var keyPrefixes = []string{"ssh-", "ecdsa-", "sk-"}

// Dir returns the SSH directory, honouring DOJO_SSH_DIR (used by tests). OpenSSH on
// Windows reads %USERPROFILE%\.ssh, which is what os.UserHomeDir returns there.
func Dir() (string, error) {
	if d := os.Getenv("DOJO_SSH_DIR"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".ssh"), nil
}

// Install writes block to dir/dojo_config and makes sure dir/config includes it.
// The Include goes at the very top so a broader Host pattern earlier in the file
// cannot override the dojo host. The first edit keeps a backup, and every write is
// atomic so a crash never truncates the learner's config.
func Install(dir, block string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(dir, FileName), []byte(block)); err != nil {
		return err
	}
	cfg, err := resolve(filepath.Join(dir, "config"))
	if err != nil {
		return err
	}
	current, err := os.ReadFile(cfg) //nolint:gosec // the learner's own SSH config
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if hasInclude(current) {
		return nil
	}
	if len(current) > 0 {
		backup := filepath.Join(dir, BackupName)
		if _, err := os.Stat(backup); errors.Is(err, fs.ErrNotExist) {
			if err := writeAtomic(backup, current); err != nil {
				return err
			}
		}
	}
	newline := "\n"
	if bytes.Contains(current, []byte("\r\n")) {
		newline = "\r\n"
	}
	var prefix []byte
	if bytes.HasPrefix(current, bom) {
		prefix, current = bom, current[len(bom):]
	}
	updated := append(append(prefix, includeLine+newline+newline...), current...)
	return writeAtomic(cfg, updated)
}

// Remove empties the dojo file and leaves the learner's config untouched.
func Remove(dir string) error {
	path := filepath.Join(dir, FileName)
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return writeAtomic(path, nil)
}

// hasInclude reports whether the config already has our Include line. ssh keywords
// are case-insensitive and Windows editors leave CRLF, which TrimSpace absorbs.
func hasInclude(cfg []byte) bool {
	cfg = bytes.TrimPrefix(cfg, bom)
	for _, line := range bytes.Split(cfg, []byte("\n")) {
		if strings.EqualFold(string(bytes.TrimSpace(line)), includeLine) {
			return true
		}
	}
	return false
}

// resolve follows a symlinked config (common with dotfile managers) so the atomic
// rename replaces the real file instead of the link.
func resolve(path string) (string, error) {
	real, err := filepath.EvalSymlinks(path)
	if errors.Is(err, fs.ErrNotExist) {
		return path, nil
	}
	return real, err
}

// writeAtomic writes through a temp file in the same directory, then renames it
// over path. The result is 0600 on Unix; Windows ignores the mode bits.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// ValidatePublicKey checks that pub is a single-line OpenSSH public key.
func ValidatePublicKey(pub []byte) error {
	pub = bytes.TrimSpace(pub)
	if bytes.ContainsAny(pub, "\r\n") {
		return errors.New("an SSH public key is a single line")
	}
	for _, p := range keyPrefixes {
		if bytes.HasPrefix(pub, []byte(p)) {
			return nil
		}
	}
	return errors.New("that does not look like an SSH public key")
}

// FindKey returns the first default key in dir: private key path and public key.
func FindKey(dir string) (private string, public []byte, err error) {
	for _, name := range KeyNames {
		if priv, pub, err := loadKey(filepath.Join(dir, name)); err == nil {
			return priv, pub, nil
		}
	}
	return "", nil, ErrNoKey
}

// ResolveKey returns the key to use: chosen (a private or .pub path, "~/" allowed)
// when set, otherwise the first default key in dir. The private key path is
// absolute, so the SSH config works from any directory.
func ResolveKey(dir, chosen string) (private string, public []byte, err error) {
	if chosen == "" {
		return FindKey(dir)
	}
	if strings.HasPrefix(chosen, "~/") || strings.HasPrefix(chosen, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", nil, err
		}
		chosen = filepath.Join(home, chosen[2:])
	}
	chosen, err = filepath.Abs(strings.TrimSuffix(chosen, ".pub"))
	if err != nil {
		return "", nil, err
	}
	return loadKey(chosen)
}

// loadKey checks that the private key file exists and reads only its .pub.
func loadKey(priv string) (string, []byte, error) {
	if _, err := os.Stat(priv); err != nil {
		return "", nil, fmt.Errorf("private key %s: %w", priv, err)
	}
	pub, err := os.ReadFile(priv + ".pub") //nolint:gosec // only the public half is ever read
	if err != nil {
		return "", nil, fmt.Errorf("public key %s.pub: %w", priv, err)
	}
	pub = bytes.TrimSpace(pub)
	if err := ValidatePublicKey(pub); err != nil {
		return "", nil, fmt.Errorf("%s.pub: %w", priv, err)
	}
	return priv, pub, nil
}
