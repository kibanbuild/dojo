package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kibanbuild/dojo/internal/state"
)

const gitTimeout = 20 * time.Second

// repoLocationVars are the variables a git hook exports to point git at one repository; dojo
// always names the clone with -C, so they must not leak in.
var repoLocationVars = map[string]bool{
	"GIT_DIR": true, "GIT_WORK_TREE": true, "GIT_INDEX_FILE": true, "GIT_OBJECT_DIRECTORY": true,
	"GIT_COMMON_DIR": true, "GIT_PREFIX": true, "GIT_NAMESPACE": true, "GIT_ALTERNATE_OBJECT_DIRECTORIES": true,
}

// isGitDir reports whether dir holds a git checkout.
func isGitDir(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// resolveClone finds the learner's practice clone (design 3.9): the path dojo check --repo
// remembered, else $HOME/dojo. needClone is true when the default is absent and the first pass
// should clone it. A remembered path that is gone, or a default that is not a clone of
// <login>/dojo (the repo name only while the login is unknown), is an error and is left alone.
func resolveClone(st *state.State, login string) (dir string, needClone bool, err error) {
	if st.Repo != "" {
		if !isGitDir(st.Repo) {
			return "", false, fmt.Errorf("your practice clone %s is gone (dojo check --repo PATH picks another)", st.Repo)
		}
		return st.Repo, false, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false, err
	}
	dir = filepath.Join(home, "dojo")
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return dir, true, nil
	} else if err != nil {
		return "", false, err
	}
	if !isGitDir(dir) || !originIs(gitOrigin(dir), login) {
		return "", false, fmt.Errorf("%s is not a clone of your practice repo, so dojo left it alone (dojo check --repo PATH points at your clone)", dir)
	}
	return dir, false, nil
}

func gitOrigin(dir string) string {
	cmd := exec.Command("git", "-C", dir, "config", "--get", "remote.origin.url") //nolint:gosec // G204: dir is the learner's own clone path
	cmd.Env = gitEnv()
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// originIs reports whether url names <login>/dojo, case aside; without a login, any repo named dojo.
func originIs(url, login string) bool {
	u := strings.TrimSuffix(strings.TrimSuffix(strings.ToLower(strings.TrimSpace(url)), "/"), ".git")
	want := "dojo"
	if login != "" {
		want = strings.ToLower(login) + "/dojo"
	}
	return strings.HasSuffix(u, "/"+want) || strings.HasSuffix(u, ":"+want)
}

func gitEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); !repoLocationVars[k] {
			env = append(env, kv)
		}
	}
	return env
}

// gitRun runs git without a terminal and with a time limit, so a passphrase or host-key prompt
// fails fast instead of hanging dojo.
type gitRun struct {
	dir string
	env []string
}

func newGit(dir string) *gitRun {
	env := append(gitEnv(), "GIT_TERMINAL_PROMPT=0")
	if os.Getenv("GIT_SSH_COMMAND") == "" && os.Getenv("GIT_SSH") == "" {
		probe := exec.Command("git", "config", "--get", "core.sshCommand")
		if dir != "" && isGitDir(dir) {
			probe = exec.Command("git", "-C", dir, "config", "--get", "core.sshCommand") //nolint:gosec // G204: the learner's own clone path
		}
		probe.Env = env
		if out, err := probe.Output(); err != nil || strings.TrimSpace(string(out)) == "" {
			env = append(env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
		}
	}
	return &gitRun{dir: dir, env: env}
}

// run returns git's combined output; stdin is closed.
func (g *gitRun) run(ctx context.Context, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	if g.dir != "" {
		args = append([]string{"-C", g.dir}, args...)
	}
	cmd := exec.CommandContext(cctx, "git", args...) //nolint:gosec // G204: fixed git verbs, the learner's own paths
	cmd.Env = g.env
	cmd.WaitDelay = time.Second
	detach(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil && errors.Is(cctx.Err(), context.DeadlineExceeded) {
		return string(out), fmt.Errorf("git took more than %s (waiting for a passphrase or a host key?)", gitTimeout)
	}
	return strings.TrimSpace(string(out)), err
}

// gitReason is the part of a failed git run that says why.
func gitReason(out string, err error) string {
	r := err.Error()
	if out != "" && !strings.Contains(r, "took more than") {
		r = lastLine(out)
	}
	r = strings.TrimPrefix(strings.TrimPrefix(r, "fatal: "), "error: ")
	return cutChars(r, 200)
}
