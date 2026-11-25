package doctor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// DefaultGitURL is the base of the default clone's URL when DOJO_GITHUB_GIT_URL is unset.
const DefaultGitURL = "git@github.com:"

// ProbeGitHub reads what key_auth needs: whether the practice clone ($HOME/dojo, or the default
// clone when none exists yet) goes over SSH and, if so, the public keys ssh offers for github.com,
// from `ssh -G` identity files and `ssh-add -L`. It reads public keys only.
func ProbeGitHub(ctx context.Context, m *Machine, gitURLBase string) {
	origin := gitURLBase
	if origin == "" {
		origin = DefaultGitURL
	}
	home, _ := os.UserHomeDir()
	if home != "" {
		dir := filepath.Join(home, "dojo")
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			out, err := runCmd(ctx, func(c context.Context) *exec.Cmd {
				return exec.CommandContext(c, "git", "-C", dir, "remote", "get-url", "origin") //nolint:gosec // dir is $HOME/dojo, an argument not a shell
			})
			if err == nil && strings.TrimSpace(out) != "" {
				origin = strings.TrimSpace(out)
			}
		}
	}
	m.CloneOverSSH = isSSHURL(origin)
	if m.CloneOverSSH {
		m.SSHOfferedKeys = offeredKeys(ctx, home)
	}
}

// isSSHURL reports an ssh:// URL or an scp-like one (git@github.com:owner/repo.git).
func isSSHURL(u string) bool {
	if strings.HasPrefix(u, "ssh://") {
		return true
	}
	if strings.Contains(u, "://") {
		return false
	}
	colon, slash := strings.Index(u, ":"), strings.Index(u, "/")
	return colon > 1 && (slash < 0 || colon < slash)
}

func offeredKeys(ctx context.Context, home string) []string {
	var keys []string
	if out, err := runCmd(ctx, func(c context.Context) *exec.Cmd { return exec.CommandContext(c, "ssh", "-G", "github.com") }); err == nil {
		for line := range strings.SplitSeq(out, "\n") {
			name, path, ok := strings.Cut(strings.TrimSpace(line), " ")
			path = strings.TrimSpace(path)
			if !ok || strings.ToLower(name) != "identityfile" || path == "none" {
				continue
			}
			if home != "" && (path == "~" || strings.HasPrefix(path, "~/")) {
				path = filepath.Join(home, path[1:])
			}
			if !filepath.IsAbs(path) {
				continue
			}
			if line, ok := readPub(path + ".pub"); ok {
				keys = append(keys, line)
			}
		}
	}
	if out, err := runCmd(ctx, func(c context.Context) *exec.Cmd { return exec.CommandContext(c, "ssh-add", "-L") }); err == nil {
		for line := range strings.SplitSeq(out, "\n") {
			if isPubLine(strings.TrimSpace(line)) {
				keys = append(keys, strings.TrimSpace(line))
			}
		}
	}
	return keys
}
