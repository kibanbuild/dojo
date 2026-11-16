package sshconfig

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const block = "Host dojo\n  Hostname 127.0.0.1\n"

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestInstallCreatesConfig(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".ssh")
	if err := Install(dir, block); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(dir, "config")); got != "Include dojo_config\n\n" {
		t.Errorf("config = %q", got)
	}
	if got := read(t, filepath.Join(dir, FileName)); got != block {
		t.Errorf("dojo_config = %q", got)
	}
}

func TestInstallPrependsOnceAndKeepsUserConfig(t *testing.T) {
	dir := t.TempDir()
	user := "Host *\n  User someone\n"
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(user), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := Install(dir, block); err != nil {
			t.Fatal(err)
		}
	}
	got := read(t, filepath.Join(dir, "config"))
	if !strings.HasPrefix(got, "Include dojo_config\n") {
		t.Errorf("include is not at the top: %q", got)
	}
	if strings.Count(got, "Include dojo_config") != 1 {
		t.Errorf("include added more than once: %q", got)
	}
	if !strings.HasSuffix(got, user) {
		t.Errorf("user config changed: %q", got)
	}
}

func TestRemoveEmptiesOnlyDojoFile(t *testing.T) {
	dir := t.TempDir()
	if err := Install(dir, block); err != nil {
		t.Fatal(err)
	}
	if err := Remove(dir); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(dir, FileName)); got != "" {
		t.Errorf("dojo_config = %q, want empty", got)
	}
	if got := read(t, filepath.Join(dir, "config")); !strings.Contains(got, "Include dojo_config") {
		t.Errorf("config lost the include: %q", got)
	}
}

func TestFindKeyPrefersEd25519(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := FindKey(dir); !errors.Is(err, ErrNoKey) {
		t.Fatalf("empty dir: err = %v, want ErrNoKey", err)
	}
	for _, name := range []string{"id_rsa", "id_ed25519"} {
		os.WriteFile(filepath.Join(dir, name), []byte("private"), 0o600)
		os.WriteFile(filepath.Join(dir, name+".pub"), []byte("ssh-x "+name+"\n"), 0o644)
	}
	priv, pub, err := FindKey(dir)
	if err != nil || filepath.Base(priv) != "id_ed25519" || string(pub) != "ssh-x id_ed25519" {
		t.Errorf("FindKey = %q, %q, %v", priv, pub, err)
	}
}

func TestFindKeyNeedsBothHalves(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "id_ed25519.pub"), []byte("ssh-ed25519 AAA"), 0o644)
	if _, _, err := FindKey(dir); !errors.Is(err, ErrNoKey) {
		t.Errorf("public key alone: err = %v, want ErrNoKey", err)
	}
}

func TestResolveKeyHonoursChoice(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"id_ed25519", "work_key"} {
		os.WriteFile(filepath.Join(dir, name), []byte("private"), 0o600)
		os.WriteFile(filepath.Join(dir, name+".pub"), []byte("ssh-ed25519 "+name), 0o644)
	}
	if priv, _, err := ResolveKey(dir, ""); err != nil || filepath.Base(priv) != "id_ed25519" {
		t.Errorf("default: %q, %v", priv, err)
	}
	chosen := filepath.Join(dir, "work_key")
	for _, in := range []string{chosen, chosen + ".pub"} {
		priv, pub, err := ResolveKey(dir, in)
		if err != nil || priv != chosen || string(pub) != "ssh-ed25519 work_key" {
			t.Errorf("ResolveKey(%q) = %q, %q, %v", in, priv, pub, err)
		}
	}
	if _, _, err := ResolveKey(dir, filepath.Join(dir, "missing")); err == nil {
		t.Error("a missing chosen key should fail, not fall back")
	}
}

func TestInstallKeepsCRLFAndBacksUpOnce(t *testing.T) {
	dir := t.TempDir()
	user := "Host *\r\n  User someone\r\n"
	os.WriteFile(filepath.Join(dir, "config"), []byte(user), 0o600)
	if err := Install(dir, block); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(dir, "config")); got != "Include dojo_config\r\n\r\n"+user {
		t.Errorf("config = %q", got)
	}
	if got := read(t, filepath.Join(dir, BackupName)); got != user {
		t.Errorf("backup = %q", got)
	}
	os.WriteFile(filepath.Join(dir, "config"), []byte("changed later\n"), 0o600)
	Install(dir, block)
	if got := read(t, filepath.Join(dir, BackupName)); got != user {
		t.Errorf("backup overwritten: %q", got)
	}
}

func TestInstallFollowsSymlinkedConfig(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(t.TempDir(), "dotfiles-ssh-config")
	os.WriteFile(real, []byte("Host x\n"), 0o600)
	if err := os.Symlink(real, filepath.Join(dir, "config")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if err := Install(dir, block); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(filepath.Join(dir, "config")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Error("the symlink was replaced by a regular file")
	}
	if got := read(t, real); !strings.HasPrefix(got, "Include dojo_config") {
		t.Errorf("real file not updated: %q", got)
	}
}

func TestValidatePublicKey(t *testing.T) {
	for _, ok := range []string{"ssh-ed25519 AAAA x", "ecdsa-sha2-nistp256 AAAA", "sk-ssh-ed25519@openssh.com AAAA", "ssh-rsa AAAA\n"} {
		if err := ValidatePublicKey([]byte(ok)); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "hello", "ssh-ed25519 AAAA\nrm -rf /", "-----BEGIN OPENSSH PRIVATE KEY-----"} {
		if err := ValidatePublicKey([]byte(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestRemoveMissingFileCreatesNothing(t *testing.T) {
	dir := t.TempDir()
	if err := Remove(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); err == nil {
		t.Error("Remove created the file")
	}
}

func TestInstallMatchesIncludeCaseInsensitively(t *testing.T) {
	dir := t.TempDir()
	user := "  include DOJO_CONFIG\r\nHost x\r\n"
	os.WriteFile(filepath.Join(dir, "config"), []byte(user), 0o600)
	if err := Install(dir, block); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(dir, "config")); got != user {
		t.Errorf("config changed: %q", got)
	}
}

func TestInstallPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows ignores mode bits")
	}
	dir := filepath.Join(t.TempDir(), ".ssh")
	if err := Install(dir, block); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{
		dir:                          0o700,
		filepath.Join(dir, "config"): 0o600,
		filepath.Join(dir, FileName): 0o600,
	} {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != want {
			t.Errorf("%s: %v, %v; want %v", path, info, err, want)
		}
	}
}

func TestDirHonoursOverride(t *testing.T) {
	t.Setenv("DOJO_SSH_DIR", "/x/ssh")
	if got, err := Dir(); err != nil || got != "/x/ssh" {
		t.Errorf("Dir = %q, %v", got, err)
	}
	t.Setenv("DOJO_SSH_DIR", "")
	t.Setenv("HOME", "/h")
	t.Setenv("USERPROFILE", `C:\h`)
	if got, err := Dir(); err != nil || filepath.Base(got) != ".ssh" {
		t.Errorf("Dir = %q, %v", got, err)
	}
}

func TestInstallKeepsBOMFirst(t *testing.T) {
	dir := t.TempDir()
	user := "\xef\xbb\xbfHost x\r\n"
	os.WriteFile(filepath.Join(dir, "config"), []byte(user), 0o600)
	if err := Install(dir, block); err != nil {
		t.Fatal(err)
	}
	want := "\xef\xbb\xbfInclude dojo_config\r\n\r\nHost x\r\n"
	if got := read(t, filepath.Join(dir, "config")); got != want {
		t.Errorf("config = %q, want %q", got, want)
	}
}

func TestInstallBOMIsIdempotent(t *testing.T) {
	for _, nl := range []string{"\n", "\r\n"} {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "config"), []byte("\xef\xbb\xbfHost foo"+nl+"  Port 5"+nl), 0o600)
		for i := 0; i < 3; i++ {
			if err := Install(dir, block); err != nil {
				t.Fatal(err)
			}
		}
		got := read(t, filepath.Join(dir, "config"))
		if strings.Count(got, "Include dojo_config") != 1 || !strings.HasPrefix(got, "\xef\xbb\xbfInclude dojo_config") || strings.Count(got, "\xef\xbb\xbf") != 1 {
			t.Errorf("nl %q: config = %q", nl, got)
		}
	}
}
