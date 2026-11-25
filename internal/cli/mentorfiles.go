package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kibanbuild/dojo/internal/mentor"
)

// cloneDir is the default local clone of the learner's practice repo (design 3.9).
func cloneDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "dojo"), nil
}

// isPracticeClone reports whether dir is a git clone whose origin is named dojo. Only the repo
// name is checked: the login is not known offline.
func isPracticeClone(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return false
	}
	cmd := exec.Command("git", "-C", dir, "config", "--get", "remote.origin.url") //nolint:gosec // G204: dir is the learner's own clone path
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	url := strings.TrimSuffix(strings.TrimSpace(string(out)), "/")
	url = strings.TrimSuffix(strings.ToLower(url), ".git")
	return strings.HasSuffix(url, "/dojo") || strings.HasSuffix(url, ":dojo")
}

// seedMentorFiles writes AGENTS.md and CLAUDE.md into the local practice clone when it exists and
// the file is absent. It never overwrites and never stages: the files stay local. Failures only warn.
func (a *App) seedMentorFiles() {
	dir, err := cloneDir()
	if err != nil || !isPracticeClone(dir) {
		return
	}
	files := []struct{ name, text string }{{"AGENTS.md", mentor.AgentsMD()}, {"CLAUDE.md", mentor.ClaudeMD}}
	for _, f := range files {
		if _, err := os.Lstat(filepath.Join(dir, f.name)); err == nil {
			return // either file present: the learner's choice stands, write nothing
		}
	}
	var added []string
	for _, f := range files {
		err := writeIfAbsent(filepath.Join(dir, f.name), f.text)
		switch {
		case err == nil:
			added = append(added, f.name)
		case errors.Is(err, fs.ErrExist):
		default:
			fmt.Fprintf(a.Out, "Warning: %s was not written in %s: %v\n", f.name, dir, err)
		}
	}
	if len(added) > 0 {
		fmt.Fprintf(a.Out, "Mentor rules (%s) added to %s. They stay local: dojo never commits them.\n", strings.Join(added, ", "), dir)
	}
}

func writeIfAbsent(path, text string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) //nolint:gosec // G302: a plain text file in the learner's own repo
	if err != nil {
		return err
	}
	if _, err := f.WriteString(text); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
