package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/kibanbuild/dojo/internal/catalog"
	"github.com/kibanbuild/dojo/internal/doctor"
	"github.com/kibanbuild/dojo/internal/state"
)

var (
	loginRE = regexp.MustCompile(`^[A-Za-z0-9-]{1,39}$`)
	shaHex  = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// resultMD is the RESULT.md of a pass: the title, when, how many checks, the catalog and the CLI.
func (a *App) resultMD(k *catalog.Ticket, out []byte) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\nPassed: %s\n", k.Title, a.now().UTC().Format("2006-01-02T15:04:05Z07:00"))
	if p, t, ok := checkCounts(out); ok {
		fmt.Fprintf(&b, "Checks: %d of %d\n", p, t)
	}
	if v := catalogVersion(k.ID); v != "" {
		fmt.Fprintf(&b, "Catalog version: %s\n", v)
	}
	fmt.Fprintf(&b, "Dojo CLI: %s\n", cleanVersion(Version))
	return b.String()
}

// myLogin asks the API for the account's GitHub login and remembers it; offline it is the last one known.
func (a *App) myLogin(ctx context.Context, st *state.State, token string) string {
	if token != "" {
		mctx, cancel := context.WithTimeout(ctx, meTimeout)
		defer cancel()
		if r, err := a.callAPI(mctx, http.MethodGet, a.telemetryAPI(), "/v1/me", token, nil); err == nil && r.status == http.StatusOK {
			var me struct {
				Login string `json:"github_login"`
			}
			if json.Unmarshal(r.body, &me) == nil && loginRE.MatchString(me.Login) {
				st.GithubLogin = me.Login
			}
		}
	}
	return st.GithubLogin
}

// passedCommitState reads progress.passed_commit_state of a ticket; "" when absent or unreadable.
func (a *App) passedCommitState(ctx context.Context, token, id string) string {
	mctx, cancel := context.WithTimeout(ctx, meTimeout)
	defer cancel()
	r, err := a.callAPI(mctx, http.MethodGet, a.telemetryAPI(), "/v1/tickets/"+escapeID(id), token, nil)
	if err != nil || r.status != http.StatusOK {
		return ""
	}
	var t struct {
		Progress struct {
			State *string `json:"passed_commit_state"`
		} `json:"progress"`
	}
	if json.Unmarshal(r.body, &t) != nil || t.Progress.State == nil {
		return ""
	}
	return *t.Progress.State
}

func (a *App) noCommit(format string, args ...any) {
	fmt.Fprintf(a.Out, "Passed. No signed commit was made: "+format+"\n", args...)
}

// passCommit makes the signed commit of a pass in the learner's clone and pushes it (design 3.9).
// The pass already counted: nothing here fails the command.
func (a *App) passCommit(ctx context.Context, st *state.State, k *catalog.Ticket, eventID string, checkOut []byte) {
	if !a.eventsEnabled() {
		return
	}
	token, _ := state.LoadToken()
	if st.Commits[k.ID] != "" && (token == "" || a.passedCommitState(ctx, token, k.ID) != "unverified") {
		return
	}
	login := a.myLogin(ctx, st, token)
	dir, needClone, err := resolveClone(st, login)
	if err == nil && needClone && login == "" {
		err = errors.New("dojo does not know your GitHub login yet (dojo login)")
	}
	if err != nil {
		a.noCommit("%v.", err)
		return
	}
	if needClone {
		if err := a.cloneDefault(ctx, dir, login); err != nil {
			a.noCommit("%v.", err)
			return
		}
	}
	g := newGit(dir)
	if br, err := g.run(ctx, "symbolic-ref", "--short", "HEAD"); err != nil || br != "main" {
		a.noCommit("the clone %s is on branch %q, not main.", dir, br)
		return
	}
	_, _ = g.run(ctx, "pull", "--ff-only")
	rel := filepath.FromSlash(k.ID) + string(filepath.Separator) + "RESULT.md"
	full := filepath.Join(dir, rel)
	text := a.resultMD(k, checkOut)
	if p := symlinkIn(dir, rel); p != "" {
		a.noCommit("%s is a symbolic link, and dojo does not write through links.", p)
		return
	}
	old, _ := os.ReadFile(full) //nolint:gosec // G304: inside the learner's clone
	err = os.MkdirAll(filepath.Dir(full), 0o750)
	if err == nil {
		err = os.WriteFile(full, []byte(text), 0o644) //nolint:gosec // G306: a plain text file in the learner's own repo
	}
	if err != nil {
		a.noCommit("%v.", err)
		return
	}
	if out, err := g.run(ctx, "add", "--", rel); err != nil {
		a.noCommit("%s.", gitReason(out, err))
		return
	}
	msg := "Dojo-Ticket: " + k.ID
	if eventID != "" {
		msg += "\nDojo-Event: " + eventID
	}
	args := []string{"commit", "-S", "--only", "-m", "pass: " + k.ID, "-m", msg}
	if bytes.Equal(old, []byte(text)) {
		args = append(args, "--allow-empty")
	}
	if out, err := g.run(ctx, append(args, "--", rel)...); err != nil {
		a.signFailed(ctx, g, out, err)
		return
	}
	sha, err := g.run(ctx, "rev-parse", "HEAD")
	if err != nil || !shaHex.MatchString(sha) {
		a.noCommit("git did not say which commit it made.")
		return
	}
	if st.Commits == nil {
		st.Commits = map[string]string{}
	}
	st.Commits[k.ID] = sha
	a.recordEvent(state.ProgressEvent{Type: "ticket.commit", ItemID: k.ID, Commit: sha})
	a.pushTried = true
	if out, err := g.run(ctx, "push", "origin", "main"); err != nil {
		st.PushPending++
		fmt.Fprintf(a.Out, "Passed. The signed commit %s is saved in %s but not pushed: %s. dojo tries again on its next run.\n", sha[:7], dir, gitReason(out, err))
	} else {
		st.PushPending = 0
		fmt.Fprintf(a.Out, "Signed commit %s pushed to main.\n", sha[:7])
	}
	_ = a.save(st)
}

// symlinkIn returns the first component of rel, under root, that is a symbolic link; "" when none.
func symlinkIn(root, rel string) string {
	p := root
	for part := range strings.SplitSeq(rel, string(filepath.Separator)) {
		p = filepath.Join(p, part)
		if fi, err := os.Lstat(p); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return p
		}
	}
	return ""
}

func (a *App) cloneDefault(ctx context.Context, dir, login string) error {
	base := a.getenv("DOJO_GITHUB_GIT_URL")
	if base == "" {
		base = doctor.DefaultGitURL
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o750); err != nil {
		return err
	}
	if out, err := newGit("").run(ctx, "clone", base+login+"/dojo.git", dir); err != nil {
		return fmt.Errorf("could not clone your practice repo: %s", gitReason(out, err))
	}
	return nil
}

// signFailed explains a commit that did not happen. dojo never commits unsigned.
func (a *App) signFailed(ctx context.Context, g *gitRun, out string, err error) {
	fmt.Fprintf(a.Out, "Passed. The signed commit was not made: %s. Run dojo doctor.\n", gitReason(out, err))
	low := strings.ToLower(out)
	if strings.Contains(low, "passphrase") || strings.Contains(low, "agent") || strings.Contains(low, "incorrect") {
		key, _ := g.run(ctx, "config", "--get", "user.signingkey")
		if key == "" || strings.Contains(key, " ") || strings.HasPrefix(key, "key::") {
			key = "<your key>"
		}
		fmt.Fprintf(a.Out, "If your key is locked, load it with: ssh-add %s\nThen run dojo check again.\n", key)
	}
}

// retryPush pushes signed commits that an earlier push could not (design 3.9). Quiet unless it works.
func (a *App) retryPush(ctx context.Context) {
	if a.pushTried || ctx.Err() != nil {
		return
	}
	st, err := state.Load()
	if err != nil || st.PushPending == 0 {
		return
	}
	dir, needClone, err := resolveClone(st, st.GithubLogin)
	if err != nil || needClone {
		return
	}
	a.pushTried = true
	if _, err := newGit(dir).run(ctx, "push", "origin", "main"); err != nil {
		return
	}
	n := st.PushPending
	st.PushPending = 0
	if a.save(st) == nil {
		fmt.Fprintf(a.Out, "Pushed %d signed commit%s that %s waiting.\n", n, plural(n), map[bool]string{true: "was", false: "were"}[n == 1])
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// pushLine is the status line for commits waiting to be pushed; "" when none.
func pushLine(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf("Commits: %d signed commit%s waiting to be pushed", n, plural(n))
}
