package fakegithub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	shaRE  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
)

const gitTimeout = 5 * time.Second

// runGit runs git against one bare repository with a clean environment: nothing the caller exported
// (GIT_DIR and friends) reaches it, and no global or system configuration is read.
func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	full := append([]string{"--git-dir", dir, "-c", "safe.directory=*"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...) //nolint:gosec // fixed binary, arguments validated by the callers
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	out, err := cmd.Output()
	return string(out), err
}

// gitDir is <GitRoot>/<login>/<repo>.git, or false when no root is configured or the names are not safe.
func (s *server) gitDir(a *account, repo Repo) (string, bool) {
	if s.cfg.GitRoot == "" || !nameRE.MatchString(a.login) || !nameRE.MatchString(repo.Name) {
		return "", false
	}
	return filepath.Join(s.cfg.GitRoot, a.login, repo.Name+".git"), true
}

// commit is GET /repos/{owner}/{repo}/commits/{sha}, answered from the bare repository with git.
func (s *server) commit(w http.ResponseWriter, r *http.Request, a *account) {
	repo, ok := publicRepo(w, r, a)
	if !ok {
		return
	}
	sha := r.PathValue("sha")
	dir, ok := s.gitDir(a, repo)
	if !ok || !shaRE.MatchString(sha) {
		apiError(w, http.StatusNotFound, "Not Found")
		return
	}
	raw, err := runGit(r.Context(), dir, "cat-file", "commit", sha)
	if err != nil {
		apiError(w, http.StatusNotFound, "Not Found")
		return
	}
	head, message, _ := strings.Cut(raw, "\n\n")
	verified, reason := false, "unsigned"
	if strings.Contains("\n"+head, "\ngpgsig ") {
		var verr error
		if verified, reason, verr = s.verifyCommit(r.Context(), dir, a, sha); verr != nil {
			apiError(w, http.StatusInternalServerError, verr.Error())
			return
		}
	}
	writeJSON(w, map[string]any{"sha": sha, "commit": map[string]any{"message": message,
		"verification": map[string]any{"verified": verified, "reason": reason}}})
}

// verifyCommit asks git whether the signature is good for one of the account's seeded signing keys.
// The signers file goes to the temp dir, never under the git root. An error is the fake's own failure.
func (s *server) verifyCommit(ctx context.Context, dir string, a *account, sha string) (bool, string, error) {
	var signers strings.Builder
	for _, k := range a.signing {
		if f := strings.Fields(k.Key); len(f) >= 2 {
			signers.WriteString(a.login + " " + f[0] + " " + f[1] + "\n")
		}
	}
	if signers.Len() == 0 {
		return false, "no_user", nil
	}
	file, err := os.CreateTemp(os.TempDir(), "allowed-signers-*")
	if err != nil {
		return false, "", fmt.Errorf("allowed signers file: %w", err)
	}
	defer func() { _ = os.Remove(file.Name()) }()
	_, werr := file.WriteString(signers.String())
	if cerr := file.Close(); werr != nil || cerr != nil {
		return false, "", fmt.Errorf("allowed signers file: %w", errors.Join(werr, cerr))
	}
	if _, err := runGit(ctx, dir, "-c", "gpg.format=ssh", "-c", "gpg.ssh.allowedSignersFile="+file.Name(), "verify-commit", sha); err != nil {
		return false, "unknown_key", nil
	}
	return true, "valid", nil
}

// compare is GET /repos/{owner}/{repo}/compare/{base}...{head}: base is a branch, head a sha.
func (s *server) compare(w http.ResponseWriter, r *http.Request, a *account) {
	repo, ok := publicRepo(w, r, a)
	if !ok {
		return
	}
	base, head, _ := strings.Cut(r.PathValue("spec"), "...")
	dir, ok := s.gitDir(a, repo)
	if !ok || !nameRE.MatchString(base) || strings.Contains(base, "..") || !shaRE.MatchString(head) {
		apiError(w, http.StatusNotFound, "Not Found")
		return
	}
	ref := "refs/heads/" + base
	count := func(spec string) (int, bool) {
		out, err := runGit(r.Context(), dir, "rev-list", "--count", spec)
		n, cerr := strconv.Atoi(strings.TrimSpace(out))
		return n, err == nil && cerr == nil
	}
	_, e1 := runGit(r.Context(), dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	_, e2 := runGit(r.Context(), dir, "cat-file", "-e", head+"^{commit}")
	ahead, ok1 := count(ref + ".." + head)
	behind, ok2 := count(head + ".." + ref)
	if e1 != nil || e2 != nil || !ok1 || !ok2 {
		apiError(w, http.StatusNotFound, "Not Found")
		return
	}
	status := "diverged"
	switch {
	case ahead == 0 && behind == 0:
		status = "identical"
	case behind == 0:
		status = "ahead"
	case ahead == 0:
		status = "behind"
	}
	writeJSON(w, map[string]any{"status": status, "ahead_by": ahead, "behind_by": behind})
}

// rename is PUT /_fake/users/{login}/rename {"login": "new"}: the account answers to the new login (and
// keeps its id) and the old one is a 404. Only the mapping changes: the git root may be a read-only
// mount, so the test moves <GitRoot>/<old> to <GitRoot>/<new> itself.
func (s *server) rename(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	var body struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !nameRE.MatchString(body.Login) {
		apiError(w, http.StatusBadRequest, "bad JSON")
		return
	}
	s.reads.mu.Lock()
	defer s.reads.mu.Unlock()
	oldKey := strings.ToLower(r.PathValue("login"))
	a := s.reads.accounts[oldKey]
	if a == nil || s.reads.accounts[strings.ToLower(body.Login)] != nil {
		apiError(w, http.StatusNotFound, "Not Found")
		return
	}
	delete(s.reads.accounts, oldKey)
	a.login = body.Login
	s.reads.accounts[strings.ToLower(body.Login)] = a
	s.reads.logins[a.id] = body.Login
	w.WriteHeader(http.StatusNoContent)
}

// setRateLimit is PUT /_fake/rate_limit {"limit": n}: it replaces FAKE_GITHUB_RATE_LIMIT until the next
// reset. Zero is no limit, a negative number is a budget already spent for every address.
func (s *server) setRateLimit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	var body struct {
		Limit int `json:"limit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		apiError(w, http.StatusBadRequest, "bad JSON")
		return
	}
	s.reads.mu.Lock()
	s.reads.limit, s.reads.limitSet = body.Limit, true
	s.reads.hits = map[string]*window{}
	s.reads.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}
