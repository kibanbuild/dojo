package state

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ErrNoToken means no CLI token is stored: the learner is not logged in.
var ErrNoToken = errors.New("not logged in")

// TokenPath returns where the CLI token lives, beside the progress file. The keychain
// is a later card; until then DOJO_HOME holds it.
func TokenPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "token"), nil
}

// LoadToken returns the stored CLI token, or ErrNoToken.
func LoadToken() (string, error) {
	p, err := TokenPath()
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(p) //nolint:gosec // G304: p is the dojo home from the user's own environment
	if errors.Is(err, fs.ErrNotExist) {
		return "", ErrNoToken
	}
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(raw))
	if tok == "" {
		return "", ErrNoToken
	}
	return tok, nil
}

// SaveToken writes the token atomically: a 0600 temp file in a 0700 directory, synced and
// renamed over the old one, so a crash never leaves a half-written or world-readable token.
func SaveToken(token string) error {
	p, err := TokenPath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".token-*") // created 0600
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		return err
	}
	if _, err := f.WriteString(token + "\n"); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		return err
	}
	ok = true
	return nil
}

// RemoveToken deletes the stored token; a missing file is not an error.
func RemoveToken() error {
	p, err := TokenPath()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
