package fakegithub_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kibanbuild/dojo/internal/fakegithub"
)

// gitIn runs git with no GIT_* variable and no global configuration, so the test never touches another repository.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GIT_") {
			env = append(env, e)
		}
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newKey(t *testing.T, name string) (private, public string) {
	t.Helper()
	private = filepath.Join(t.TempDir(), name)
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "", "-f", private).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v %s", err, out)
	}
	b, err := os.ReadFile(private + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	return private, strings.TrimSpace(string(b))
}

type gitFixture struct {
	t    *testing.T
	root string
	work string
	url  string
}

func newGitFixture(t *testing.T, signing ...string) *gitFixture {
	t.Helper()
	for _, bin := range []string{"git", "ssh-keygen"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is not installed", bin)
		}
	}
	f := &gitFixture{t: t, root: t.TempDir(), work: t.TempDir()}
	bare := filepath.Join(f.root, "ada", "dojo.git")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, bare, "init", "--bare", "-b", "main")
	gitIn(t, f.work, "init", "-b", "main")
	gitIn(t, f.work, "remote", "add", "origin", bare)
	var keys []fakegithub.Key
	for i, k := range signing {
		keys = append(keys, fakegithub.Key{ID: int64(i + 1), Key: k})
	}
	srv := httptest.NewServer(fakegithub.New(fakegithub.Config{GitRoot: f.root, Users: []fakegithub.User{
		{ID: 1001, Login: "ada", SigningKeys: keys, Repos: []fakegithub.Repo{{Name: "dojo"}}}}}))
	t.Cleanup(srv.Close)
	f.url = srv.URL
	return f
}

// commit makes a commit in the work clone, signed with key when it is not empty, and returns its sha.
func (f *gitFixture) commit(key, message string) string {
	f.t.Helper()
	args := []string{"-c", "user.name=Ada", "-c", "user.email=ada@example.test", "-c", "gpg.format=ssh"}
	if key != "" {
		args = append(args, "-c", "user.signingkey="+key)
	}
	args = append(args, "commit", "--allow-empty", "-m", message)
	if key != "" {
		args = append(args, "-S")
	}
	gitIn(f.t, f.work, args...)
	return gitIn(f.t, f.work, "rev-parse", "HEAD")
}

func (f *gitFixture) push(ref string) { f.t.Helper(); gitIn(f.t, f.work, "push", "origin", ref) }

func (f *gitFixture) get(path string) (int, map[string]any) {
	f.t.Helper()
	resp, err := http.Get(f.url + path)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return resp.StatusCode, m
}

func verification(m map[string]any) (bool, string) {
	v := m["commit"].(map[string]any)["verification"].(map[string]any)
	return v["verified"].(bool), v["reason"].(string)
}

func TestCommitVerificationFollowsTheSeededSigningKeys(t *testing.T) {
	priv, pub := newKey(t, "ada")
	otherPriv, _ := newKey(t, "mallory")
	f := newGitFixture(t, pub)
	good := f.commit(priv, "pass: a/b\n\nDojo-Ticket: a/b")
	stranger := f.commit(otherPriv, "pass: a/b\n\nDojo-Ticket: a/b")
	unsigned := f.commit("", "plain")
	f.push("main")

	code, m := f.get("/repos/ada/dojo/commits/" + good)
	if ok, reason := verification(m); code != 200 || !ok || reason != "valid" || m["sha"] != good ||
		!strings.Contains(m["commit"].(map[string]any)["message"].(string), "Dojo-Ticket: a/b") {
		t.Fatalf("good: %d %v", code, m)
	}
	if code, m = f.get("/repos/ada/dojo/commits/" + stranger); code != 200 {
		t.Fatalf("stranger: %d", code)
	} else if ok, reason := verification(m); ok || reason != "unknown_key" {
		t.Fatalf("stranger verified: %v %s", ok, reason)
	}
	if _, m = f.get("/repos/ada/dojo/commits/" + unsigned); m == nil {
		t.Fatal("no answer")
	} else if ok, reason := verification(m); ok || reason != "unsigned" {
		t.Fatalf("unsigned: %v %s", ok, reason)
	}
}

func TestCommitAndCompareAnswer404Sensibly(t *testing.T) {
	f := newGitFixture(t)
	for _, p := range []string{
		"/repos/ada/dojo/commits/" + strings.Repeat("a", 40), // not pushed
		"/repos/ada/dojo/commits/main",
		"/repos/ada/dojo/commits/..%2f..%2fetc",
		"/repos/nobody/dojo/commits/" + strings.Repeat("a", 40),
		"/repos/ada/other/commits/" + strings.Repeat("a", 40),
		"/repos/ada/dojo/compare/main..." + strings.Repeat("a", 40),
		"/repos/ada/dojo/compare/main",
	} {
		if code, _ := f.get(p); code != 404 {
			t.Errorf("%s: %d", p, code)
		}
	}
}

func TestCompareStatuses(t *testing.T) {
	f := newGitFixture(t)
	first := f.commit("", "one")
	f.push("main")
	second := f.commit("", "two")
	f.push("main")
	gitIn(t, f.work, "checkout", "-q", "-b", "feature", first)
	feature := f.commit("", "feature")
	f.push("feature")
	cmp := func(head string) (string, float64, float64) {
		code, m := f.get("/repos/ada/dojo/compare/main..." + head)
		if code != 200 {
			t.Fatalf("compare %s: %d", head, code)
		}
		return m["status"].(string), m["ahead_by"].(float64), m["behind_by"].(float64)
	}
	if s, a, b := cmp(second); s != "identical" || a != 0 || b != 0 {
		t.Errorf("tip: %s %v %v", s, a, b)
	}
	if s, a, b := cmp(first); s != "behind" || a != 0 || b != 1 {
		t.Errorf("older: %s %v %v", s, a, b)
	}
	if s, a, b := cmp(feature); s != "diverged" || a != 1 || b != 1 {
		t.Errorf("branch: %s %v %v", s, a, b)
	}
}

func TestRenameMovesTheAccountAndItsRepos(t *testing.T) {
	f := newGitFixture(t)
	sha := f.commit("", "one")
	f.push("main")
	put := func(path, body string) int {
		req, _ := http.NewRequest(http.MethodPut, f.url+path, strings.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := put("/_fake/users/ada/rename", `{"login":"ada-two"}`); code != 204 {
		t.Fatalf("rename: %d", code)
	}
	if code, m := f.get("/user/1001"); code != 200 || m["login"] != "ada-two" {
		t.Fatalf("by id: %d %v", code, m)
	}
	if code, _ := f.get("/repos/ada/dojo/commits/" + sha); code != 404 {
		t.Fatalf("old login: %d", code)
	}
	if _, err := os.Stat(filepath.Join(f.root, "ada")); err != nil {
		t.Fatalf("the fake must not touch the git root: %v", err)
	}
	if err := os.Rename(filepath.Join(f.root, "ada"), filepath.Join(f.root, "ada-two")); err != nil {
		t.Fatal(err)
	}
	if code, _ := f.get("/repos/ada-two/dojo/commits/" + sha); code != 200 {
		t.Fatalf("new login: %d", code)
	}
}

func TestSetRateLimitSpendsTheBudget(t *testing.T) {
	f := newGitFixture(t)
	req, _ := http.NewRequest(http.MethodPut, f.url+"/_fake/rate_limit", strings.NewReader(`{"limit":-1}`))
	put, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	put.Body.Close()
	if put.StatusCode != 204 {
		t.Fatalf("put: %d", put.StatusCode)
	}
	resp, err := http.Get(f.url + "/user/1001")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 || resp.Header.Get("X-RateLimit-Remaining") != "0" {
		t.Fatalf("%d %v", resp.StatusCode, resp.Header)
	}
}
