//go:build integration

package server_test

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The verifier runs inside the API container, ticking every second (compose.it.yaml). The tests push
// signed commits into the bare repository the fake GitHub serves from DOJO_FAKE_GITHUB_GIT_DIR.

const verifyRowSQL = "SELECT coalesce(commit_state, '-') || '|' || commit_tries || '|' || (commit_checked_at IS NOT NULL)::text " +
	"FROM ticket_progress p JOIN learners l ON l.id = p.learner_id WHERE l.github_id = 1001 AND item_id = 'ev-role/one'"

const dueSQL = "UPDATE ticket_progress SET commit_next_at = now() WHERE learner_id IN (SELECT id FROM learners WHERE github_id = 1001)"

const dojoTrailer = "\n\nDojo-Ticket: ev-role/one\nDojo-Event: 0194f0f2-0000-7000-8000-000000000001"

type verifyFixture struct {
	t        *testing.T
	root     string
	bare     string
	work     string
	key      string
	otherKey string
	cli      *cliClient
	token    string
}

func gitDirEnv(t *testing.T) string {
	t.Helper()
	d := os.Getenv("DOJO_FAKE_GITHUB_GIT_DIR")
	if d == "" {
		t.Fatal("DOJO_FAKE_GITHUB_GIT_DIR is not set: run `make test-integration`")
	}
	return d
}

// gitIn runs git with no GIT_* variable and no global configuration, so it never touches another repository.
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

func newSigningKey(t *testing.T, name string) (private, public string) {
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

// newVerifyFixture signs ada up, gives her a passed ticket with no commit yet, a public dojo repo on the
// fake with her signing key, and a bare repository plus a work clone to make commits in.
func newVerifyFixture(t *testing.T) *verifyFixture {
	t.Helper()
	root := gitDirEnv(t)
	clean := func() {
		fakeControl(t, "POST", "/_fake/reset", "")
		_ = os.RemoveAll(filepath.Join(root, "ada"))
		_ = os.RemoveAll(filepath.Join(root, "ada-renamed"))
	}
	clean()
	t.Cleanup(clean)
	c, tok := eventsFixture(t)
	f := &verifyFixture{t: t, root: root, work: t.TempDir(), cli: c, token: tok}
	var pub string
	f.key, pub = newSigningKey(t, "ada")
	f.otherKey, _ = newSigningKey(t, "stranger")
	fakeControl(t, "PUT", "/_fake/users/ada/signing_keys", `[{"id":1,"key":"`+pub+`","title":"t"}]`)
	fakeControl(t, "PUT", "/_fake/users/ada/repos/dojo", `{}`)
	f.bare = filepath.Join(root, "ada", "dojo.git")
	if err := os.MkdirAll(f.bare, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, f.bare, "init", "--bare", "-b", "main")
	gitIn(t, f.work, "init", "-b", "main")
	gitIn(t, f.work, "remote", "add", "origin", f.bare)
	post(t, c, tok, evStarted(evID(), "ev-role/one", "2026-11-23T10:00:00Z"), evPass())
	return f
}

// commit makes a commit with the trailers of a pass, signed with key (empty means unsigned).
func (f *verifyFixture) commit(key, subject string) string {
	f.t.Helper()
	args := []string{"-c", "user.name=Ada", "-c", "user.email=ada@example.test", "-c", "gpg.format=ssh"}
	if key != "" {
		args = append(args, "-c", "user.signingkey="+key)
	}
	args = append(args, "commit", "--allow-empty", "-m", subject+dojoTrailer)
	if key != "" {
		args = append(args, "-S")
	}
	gitIn(f.t, f.work, args...)
	return gitIn(f.t, f.work, "rev-parse", "HEAD")
}

func (f *verifyFixture) push(ref string) { f.t.Helper(); gitIn(f.t, f.work, "push", "origin", ref) }

// report sends the ticket.commit event for sha and makes the row due now.
func (f *verifyFixture) report(sha string) {
	f.t.Helper()
	post(f.t, f.cli, f.token, evCommit(evID(), sha))
	asOwner(f.t, dueSQL)
}

// wait polls the commit columns until want says so, for at most 25 seconds.
func (f *verifyFixture) wait(what string, want func(row string) bool) string {
	f.t.Helper()
	deadline := time.Now().Add(25 * time.Second)
	for {
		row := asOwner(f.t, verifyRowSQL)
		if want(row) {
			return row
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("%s: row is %q", what, row)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func triedAtLeastOnce(row string) bool {
	return strings.HasPrefix(row, "pending|") && !strings.HasPrefix(row, "pending|0|")
}

func fakeGet(t *testing.T, path string) map[string]any {
	t.Helper()
	resp, err := http.Get(fakeURL(t) + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return m
}

func TestVerifierPendingUntilPushedThenVerified(t *testing.T) {
	f := newVerifyFixture(t)
	sha := f.commit(f.key, "pass: ev-role/one")
	f.report(sha) // committed locally, not pushed yet
	f.wait("a try counted while unpushed", triedAtLeastOnce)

	f.push("main")
	asOwner(t, dueSQL)
	row := f.wait("verified once pushed", func(r string) bool { return strings.HasPrefix(r, "verified|") })
	if !strings.HasSuffix(row, "|true") {
		t.Fatalf("commit_checked_at not set: %q", row)
	}
	code, body := f.cli.call(http.MethodGet, "/v1/tickets/ev-role/one", f.token, nil)
	if code != 200 || !strings.Contains(body, `"passed_commit_state":"verified"`) {
		t.Fatalf("learner view: %d %s", code, body)
	}
}

func TestVerifierRefusesACommitSignedByAnUnknownKey(t *testing.T) {
	f := newVerifyFixture(t)
	sha := f.commit(f.otherKey, "pass: ev-role/one")
	f.push("main")
	f.report(sha)
	f.wait("a try counted", triedAtLeastOnce)
	got := fakeGet(t, "/repos/ada/dojo/commits/"+sha)["commit"].(map[string]any)["verification"].(map[string]any)
	if got["verified"] != false {
		t.Fatalf("the fake verified a stranger's signature: %v", got)
	}
	if row := asOwner(t, verifyRowSQL); !strings.HasPrefix(row, "pending|") {
		t.Fatalf("row: %q", row)
	}
}

func TestVerifierRefusesACommitNotOnMain(t *testing.T) {
	f := newVerifyFixture(t)
	f.commit(f.key, "base")
	f.push("main")
	gitIn(t, f.work, "checkout", "-q", "-b", "feature")
	sha := f.commit(f.key, "pass: ev-role/one")
	f.push("feature")
	f.report(sha)
	f.wait("a try counted", triedAtLeastOnce)
	if st := fakeGet(t, "/repos/ada/dojo/compare/main..."+sha)["status"]; st != "ahead" {
		t.Fatalf("compare status %v", st)
	}
	if row := asOwner(t, verifyRowSQL); !strings.HasPrefix(row, "pending|") {
		t.Fatalf("row: %q", row)
	}
}

func TestVerifierGivesUpAfterSevenDays(t *testing.T) {
	f := newVerifyFixture(t)
	sha := f.commit(f.key, "pass: ev-role/one") // never pushed
	post(t, f.cli, f.token, evCommit(evID(), sha))
	asOwner(t, "UPDATE ticket_progress SET commit_at = now() - interval '8 days', commit_next_at = now() WHERE learner_id IN (SELECT id FROM learners WHERE github_id = 1001)")
	f.wait("unverified after 7 days", func(r string) bool { return strings.HasPrefix(r, "unverified|") })
	if got := asOwner(t, "SELECT commit_next_at IS NULL FROM ticket_progress WHERE item_id = 'ev-role/one'"); got != "t" {
		t.Fatalf("an unverified row must leave the schedule: %q", got)
	}
}

func TestVerifierFollowsARenamedAccount(t *testing.T) {
	f := newVerifyFixture(t)
	sha := f.commit(f.key, "pass: ev-role/one")
	f.push("main")
	fakeControl(t, "PUT", "/_fake/users/ada/rename", `{"login":"ada-renamed"}`)
	// The fake only remaps the login; the mount is the test's, so the test moves the repository.
	if err := os.Rename(filepath.Join(f.root, "ada"), filepath.Join(f.root, "ada-renamed")); err != nil {
		t.Fatal(err)
	}
	f.report(sha)
	f.wait("verified under the new login", func(r string) bool { return strings.HasPrefix(r, "verified|") })
	if got := asOwner(t, "SELECT github_login FROM learners WHERE github_id = 1001"); got != "ada-renamed" {
		t.Fatalf("github_login: %q", got)
	}
}

func TestVerifierCountsNoTryWhileGitHubIsRateLimited(t *testing.T) {
	f := newVerifyFixture(t)
	sha := f.commit(f.key, "pass: ev-role/one")
	f.push("main")
	fakeControl(t, "PUT", "/_fake/rate_limit", `{"limit":-1}`)
	f.report(sha)
	deadline := time.Now().Add(25 * time.Second)
	for asOwner(t, "SELECT commit_next_at > now() + interval '4 minutes' FROM ticket_progress WHERE item_id = 'ev-role/one'") != "t" {
		if time.Now().After(deadline) {
			t.Fatal("the verifier never leased the row")
		}
		time.Sleep(250 * time.Millisecond)
	}
	time.Sleep(2 * time.Second) // let the read that follows the lease finish
	if row := asOwner(t, verifyRowSQL); row != "pending|0|false" {
		t.Fatalf("a rate-limited read must not count: %q", row)
	}

	fakeControl(t, "PUT", "/_fake/rate_limit", `{"limit":0}`)
	asOwner(t, dueSQL)
	f.wait("verified once the limit lifts", func(r string) bool { return strings.HasPrefix(r, "verified|0|") })
}
