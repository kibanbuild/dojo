package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/kibanbuild/dojo/internal/state"
)

type passAPI struct {
	srv    *httptest.Server
	mu     sync.Mutex
	state  string // passed_commit_state served on the ticket; "" serves null
	events []map[string]any
}

type passFix struct {
	e       *env
	api     *passAPI
	bare    string // ada/dojo.git
	home    string
	key     string
	signers string
}

func (f *passFix) gitconfig(t *testing.T, signingKey string) {
	t.Helper()
	cfg := "[user]\n\tname = Ada\n\temail = ada@x.dev\n\tsigningkey = " + signingKey + "\n" +
		"[gpg]\n\tformat = ssh\n[gpg \"ssh\"]\n\tallowedSignersFile = " + f.signers + "\n[init]\n\tdefaultBranch = main\n"
	if err := os.WriteFile(filepath.Join(f.home, ".gitconfig"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
}

func newPassFix(t *testing.T) *passFix {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fixture builds file:// remotes and a gitconfig from native paths")
	}
	for _, bin := range []string{"git", "ssh-keygen"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin, "not installed")
		}
	}
	api := &passAPI{}
	api.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		switch {
		case r.URL.Path == "/v1/me":
			_, _ = w.Write([]byte(`{"id":"u1","github_login":"ada"}`))
		case r.URL.Path == "/v1/tickets/white/a":
			if api.state == "" {
				_, _ = w.Write([]byte(`{"progress":{"passed_commit_state":null}}`))
			} else {
				_, _ = w.Write([]byte(`{"progress":{"passed_commit_state":"` + api.state + `"}}`))
			}
		case r.URL.Path == "/v1/events" && r.Method == http.MethodPost:
			raw, _ := io.ReadAll(r.Body)
			var body struct {
				Events []map[string]any `json:"events"`
			}
			_ = json.Unmarshal(raw, &body)
			api.events = append(api.events, body.Events...)
			_, _ = w.Write([]byte(`{"accepted":1,"duplicates":0,"ignored":0}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(api.srv.Close)

	e := newEnv(t)
	root := t.TempDir()
	f := &passFix{e: e, api: api, home: os.Getenv("HOME"), key: filepath.Join(root, "key"), signers: filepath.Join(root, "signers")}
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "ada@x.dev", "-f", f.key).CombinedOutput(); err != nil { //nolint:gosec // G204: test paths
		t.Fatalf("ssh-keygen: %v\n%s", err, out)
	}
	pub, err := os.ReadFile(f.key + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.signers, []byte("ada@x.dev "+string(pub)), 0o600); err != nil {
		t.Fatal(err)
	}
	f.gitconfig(t, f.key)

	f.bare = filepath.Join(root, "git", "ada", "dojo.git")
	if err := os.MkdirAll(f.bare, 0o750); err != nil {
		t.Fatal(err)
	}
	gitIn(t, f.bare, "init", "-q", "--bare", "-b", "main")
	seed := filepath.Join(root, "seed")
	if err := os.MkdirAll(seed, 0o750); err != nil {
		t.Fatal(err)
	}
	gitIn(t, seed, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, seed, "add", "README.md")
	gitIn(t, seed, "commit", "-q", "-m", "init")
	gitIn(t, seed, "push", "-q", f.bare, "main")

	base := "file://" + filepath.Join(root, "git") + "/"
	e.app.Login = LoginConfig{APIURL: api.srv.URL}
	e.app.Getenv = func(k string) string {
		if k == "DOJO_GITHUB_GIT_URL" {
			return base
		}
		return ""
	}
	if err := state.SaveToken("tok"); err != nil {
		t.Fatal(err)
	}
	writeSidecar(t, "white/a", "a1b2c3d4e5f6")
	st, _ := state.Load()
	st.Current = "white/a"
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *passFix) clone() string { return filepath.Join(f.home, "dojo") }

func (f *passFix) count(t *testing.T) string {
	t.Helper()
	return strings.TrimSpace(gitIn(t, f.bare, "rev-list", "--count", "main"))
}

func (f *passFix) head(t *testing.T) string {
	t.Helper()
	return strings.TrimSpace(gitIn(t, f.bare, "rev-parse", "main"))
}

func (f *passFix) eventsOf(typ string) []map[string]any {
	f.api.mu.Lock()
	defer f.api.mu.Unlock()
	var out []map[string]any
	for _, ev := range f.api.events {
		if ev["type"] == typ {
			out = append(out, ev)
		}
	}
	return out
}

func (f *passFix) check(t *testing.T, args ...string) string {
	t.Helper()
	if err := f.e.run(t, append([]string{"check"}, args...)...); err != nil {
		t.Fatalf("check: %v\n%s", err, f.e.out)
	}
	return f.e.out.String()
}

func TestFirstPassClonesAndMakesASignedCommit(t *testing.T) {
	f := newPassFix(t)
	out := f.check(t)
	if !strings.Contains(out, "pushed to main") {
		t.Errorf("output:\n%s", out)
	}
	if f.count(t) != "2" {
		t.Fatalf("commits on origin = %s, want 2", f.count(t))
	}
	msg := strings.TrimSpace(gitIn(t, f.bare, "log", "-1", "--format=%B", "main")) + "\n"
	runs := f.eventsOf("check.run")
	if len(runs) != 1 {
		t.Fatalf("check.run events: %v", runs)
	}
	want := "pass: white/a\n\nDojo-Ticket: white/a\nDojo-Event: " + runs[0]["id"].(string) + "\n"
	if msg != want {
		t.Errorf("message:\n%q\nwant\n%q", msg, want)
	}
	if got := gitIn(t, f.bare, "show", "main:white/a/RESULT.md"); got != "# Ticket A\n\nPassed: 2026-11-16T12:00:00Z\nChecks: 2 of 2\nCatalog version: a1b2c3d4e5f6\nDojo CLI: dev\n" {
		t.Errorf("RESULT.md:\n%s", got)
	}
	if v := gitIn(t, f.bare, "log", "-1", "--format=%G?", "main"); strings.TrimSpace(v) != "G" {
		t.Errorf("signature status %q, want G", v)
	}
	if files := strings.TrimSpace(gitIn(t, f.bare, "show", "--name-only", "--format=", "main")); files != "white/a/RESULT.md" {
		t.Errorf("commit touched %q", files)
	}
	commits := f.eventsOf("ticket.commit")
	if len(commits) != 1 || commits[0]["commit"] != f.head(t) || commits[0]["item_id"] != "white/a" {
		t.Errorf("ticket.commit events: %v (head %s)", commits, f.head(t))
	}
	st, _ := state.Load()
	if st.Commits["white/a"] != f.head(t) || st.PushPending != 0 {
		t.Errorf("state: %+v", st)
	}
}

func TestRevisitPassAddsNoCommit(t *testing.T) {
	f := newPassFix(t)
	f.check(t)
	f.api.state = "pending"
	f.check(t)
	f.api.state = "verified"
	f.check(t)
	if f.count(t) != "2" || len(f.eventsOf("ticket.commit")) != 1 {
		t.Errorf("commits %s, ticket.commit events %d", f.count(t), len(f.eventsOf("ticket.commit")))
	}
}

func TestUnverifiedCommitIsRecommittedEvenWhenUnchanged(t *testing.T) {
	f := newPassFix(t)
	f.check(t)
	first := f.head(t)
	f.api.state = "unverified"
	f.check(t)
	if f.count(t) != "3" || f.head(t) == first {
		t.Fatalf("commits %s, head unchanged %v", f.count(t), f.head(t) == first)
	}
	commits := f.eventsOf("ticket.commit")
	if len(commits) != 2 || commits[1]["commit"] != f.head(t) {
		t.Errorf("ticket.commit events: %v", commits)
	}
	if v := gitIn(t, f.bare, "log", "-1", "--format=%G?", "main"); strings.TrimSpace(v) != "G" {
		t.Errorf("signature status %q", v)
	}
}

func TestNoSigningKeyMakesNoCommitButThePassCounts(t *testing.T) {
	f := newPassFix(t)
	f.gitconfig(t, filepath.Join(f.home, "missing-key"))
	out := f.check(t)
	if !strings.Contains(out, "Passed. The signed commit was not made:") || !strings.Contains(out, "Run dojo doctor.") {
		t.Errorf("output:\n%s", out)
	}
	if f.count(t) != "1" || len(f.eventsOf("ticket.commit")) != 0 {
		t.Errorf("a commit leaked: %s commits, %d events", f.count(t), len(f.eventsOf("ticket.commit")))
	}
	if n := strings.TrimSpace(gitIn(t, f.clone(), "rev-list", "--count", "HEAD")); n != "1" {
		t.Errorf("clone has %s commits, want 1 (never unsigned)", n)
	}
	st, _ := state.Load()
	if _, ok := st.Passed["white/a"]; !ok || st.Commits["white/a"] != "" {
		t.Errorf("state: %+v", st)
	}
	f.gitconfig(t, f.key) // fixing the key and checking again commits
	f.check(t)
	if f.count(t) != "2" {
		t.Errorf("after the fix: %s commits", f.count(t))
	}
}

func TestPushFailureKeepsTheCommitAndIsRetriedByStatus(t *testing.T) {
	f := newPassFix(t)
	f.check(t)
	good := strings.TrimSpace(gitIn(t, f.clone(), "remote", "get-url", "origin"))
	gitIn(t, f.clone(), "remote", "set-url", "origin", "file:///nonexistent/ada/dojo.git")
	f.api.state = "unverified"
	out := f.check(t)
	if !strings.Contains(out, "not pushed") {
		t.Errorf("output:\n%s", out)
	}
	if st, _ := state.Load(); st.PushPending != 1 {
		t.Errorf("push_pending = %d", st.PushPending)
	}
	if len(f.eventsOf("ticket.commit")) != 2 {
		t.Errorf("ticket.commit must be queued whatever the push did: %v", f.eventsOf("ticket.commit"))
	}
	if err := f.e.run(t, "status"); err != nil || !strings.Contains(f.e.out.String(), "1 signed commit waiting to be pushed") {
		t.Errorf("status: %v\n%s", err, f.e.out)
	}
	gitIn(t, f.clone(), "remote", "set-url", "origin", good)
	if err := f.e.run(t, "status"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.e.out.String(), "waiting to be pushed\n") && !strings.Contains(f.e.out.String(), "Pushed 1 signed commit") {
		t.Errorf("status did not push:\n%s", f.e.out)
	}
	if f.count(t) != "3" {
		t.Errorf("origin has %s commits, want 3", f.count(t))
	}
	if st, _ := state.Load(); st.PushPending != 0 {
		t.Errorf("push_pending = %d after the retry", st.PushPending)
	}
}

func TestWrongBranchMakesNoCommit(t *testing.T) {
	f := newPassFix(t)
	gitIn(t, f.home, "clone", "-q", f.bare, f.clone())
	gitIn(t, f.clone(), "checkout", "-q", "-b", "work")
	out := f.check(t)
	if !strings.Contains(out, `"work", not main`) {
		t.Errorf("output:\n%s", out)
	}
	if f.count(t) != "1" || len(f.eventsOf("ticket.commit")) != 0 {
		t.Errorf("a commit was made on the wrong branch")
	}
}

func TestForeignOriginAtTheDefaultPathIsLeftAlone(t *testing.T) {
	f := newPassFix(t)
	if err := os.MkdirAll(f.clone(), 0o750); err != nil {
		t.Fatal(err)
	}
	gitIn(t, f.clone(), "init", "-q", "-b", "main")
	gitIn(t, f.clone(), "remote", "add", "origin", "git@github.com:bob/dojo.git")
	out := f.check(t)
	if !strings.Contains(out, "left it alone") {
		t.Errorf("output:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(f.clone(), "white")); err == nil {
		t.Error("wrote into a foreign clone")
	}
	if f.count(t) != "1" {
		t.Error("pushed from a foreign clone")
	}
	// A different case of the same repo is the learner's clone.
	gitIn(t, f.clone(), "remote", "set-url", "origin", f.bare)
	gitIn(t, f.clone(), "remote", "set-url", "origin", "file://"+strings.Replace(f.bare, "/ada/", "/ADA/", 1))
	if !originIs(gitOrigin(f.clone()), "ada") {
		t.Error("origin match must ignore case")
	}
}

func TestRepoFlagIsRememberedAndUsed(t *testing.T) {
	f := newPassFix(t)
	other := filepath.Join(t.TempDir(), "mine")
	gitIn(t, f.home, "clone", "-q", f.bare, other)
	f.check(t, "--repo", other)
	if st, _ := state.Load(); st.Repo != other {
		t.Errorf("repo = %q", st.Repo)
	}
	if _, err := os.Stat(f.clone()); err == nil {
		t.Error("the default clone was made although --repo chose another")
	}
	if f.count(t) != "2" {
		t.Fatalf("commits = %s", f.count(t))
	}
	f.api.state = "unverified"
	f.check(t) // no flag: the remembered path
	if f.count(t) != "3" {
		t.Errorf("remembered repo not used: %s commits", f.count(t))
	}
}

func TestGonePathsAreErrorsNamingThem(t *testing.T) {
	f := newPassFix(t)
	gone := filepath.Join(t.TempDir(), "nowhere")
	err := f.e.run(t, "check", "--repo", gone)
	if err == nil || !strings.Contains(err.Error(), gone) {
		t.Errorf("--repo of a missing path: %v", err)
	}
	st, _ := state.Load()
	st.Repo = gone
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	out := f.check(t)
	if !strings.Contains(out, gone) || !strings.Contains(out, "is gone") {
		t.Errorf("remembered path gone:\n%s", out)
	}
	if err := f.e.run(t, "check", "--repo"); err == nil {
		t.Error("--repo without a path should fail")
	}
}

func TestOtherFilesInTheCloneAreUntouched(t *testing.T) {
	f := newPassFix(t)
	gitIn(t, f.home, "clone", "-q", f.bare, f.clone())
	notes := filepath.Join(f.clone(), "notes.txt")
	if err := os.WriteFile(notes, []byte("mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	readme := filepath.Join(f.clone(), "README.md")
	if err := os.WriteFile(readme, []byte("edited\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, f.clone(), "add", "README.md")
	f.check(t)
	if f.count(t) != "2" {
		t.Fatalf("commits = %s", f.count(t))
	}
	if files := strings.TrimSpace(gitIn(t, f.bare, "show", "--name-only", "--format=", "main")); files != "white/a/RESULT.md" {
		t.Errorf("commit touched %q", files)
	}
	if read(t, notes) != "mine\n" || read(t, readme) != "edited\n" {
		t.Error("learner files changed")
	}
	if staged := strings.TrimSpace(gitIn(t, f.clone(), "diff", "--cached", "--name-only")); staged != "README.md" {
		t.Errorf("staged = %q, want the learner's own README.md left staged", staged)
	}
}

func TestSymlinkedResultPathsAreRefused(t *testing.T) {
	for _, name := range []string{"RESULT.md", "dir"} {
		f := newPassFix(t)
		gitIn(t, f.home, "clone", "-q", f.bare, f.clone())
		victim := filepath.Join(t.TempDir(), "victim")
		if err := os.MkdirAll(victim, 0o750); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(victim, "RESULT.md")
		if err := os.WriteFile(target, []byte("keep\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(f.clone(), "white", "a", "RESULT.md")
		if name == "dir" {
			link = filepath.Join(f.clone(), "white")
		}
		if err := os.MkdirAll(filepath.Dir(link), 0o750); name == "RESULT.md" && err != nil {
			t.Fatal(err)
		}
		src := target
		if name == "dir" {
			src = victim
		}
		if err := os.Symlink(src, link); err != nil {
			t.Skip("no symlinks:", err)
		}
		out := f.check(t)
		if !strings.Contains(out, "symbolic link") || f.count(t) != "1" {
			t.Errorf("%s: output:\n%s\ncommits %s", name, out, f.count(t))
		}
		if read(t, target) != "keep\n" {
			t.Errorf("%s: wrote through the link", name)
		}
	}
}

func TestNoCommitWithoutEvents(t *testing.T) {
	f := newPassFix(t)
	f.e.app.Login = LoginConfig{}
	f.check(t)
	if _, err := os.Stat(f.clone()); err == nil {
		t.Error("cloned although no API is configured")
	}
}

func TestOriginIs(t *testing.T) {
	tests := []struct {
		url, login string
		want       bool
	}{
		{"git@github.com:ada/dojo.git", "ada", true},
		{"https://github.com/Ada/Dojo", "ada", true},
		{"https://github.com/ada/dojo.git/", "ADA", true},
		{"file:///srv/git/ada/dojo.git", "ada", true},
		{"git@github.com:bob/dojo.git", "ada", false},
		{"git@github.com:ada/notes.git", "ada", false},
		{"git@github.com:bob/dojo.git", "", true},
		{"git@github.com:bob/notdojo.git", "", false},
		{"", "ada", false},
	}
	for _, tt := range tests {
		if got := originIs(tt.url, tt.login); got != tt.want {
			t.Errorf("originIs(%q, %q) = %v", tt.url, tt.login, got)
		}
	}
}

func TestMentorFilesUseTheRememberedCloneAndTheStrictMatch(t *testing.T) {
	e := newEnv(t)
	dir := newClone(t, "git@github.com:ada/dojo.git")
	st, _ := state.Load()
	st.GithubLogin = "bob"
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	if err := e.run(t, "start", "white/a"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); err == nil {
		t.Error("seeded another account's clone")
	}
	mine := filepath.Join(t.TempDir(), "mine")
	if err := os.MkdirAll(mine, 0o750); err != nil {
		t.Fatal(err)
	}
	gitIn(t, mine, "init", "-q")
	st, _ = state.Load()
	st.Repo = mine
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	if err := e.run(t, "start", "white/a"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(mine, "AGENTS.md")); err != nil {
		t.Errorf("the remembered clone was not seeded: %v", err)
	}
}
