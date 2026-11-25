package cli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kibanbuild/dojo/internal/mentor"
	"github.com/kibanbuild/dojo/internal/state"
)

// gitIn runs git in dir with the hook-exported GIT_* variables stripped.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...) //nolint:gosec // G204: test helper with fixed args
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func newClone(t *testing.T, origin string) string {
	t.Helper()
	dir := filepath.Join(os.Getenv("HOME"), "dojo")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "init", "-q")
	gitIn(t, dir, "remote", "add", "origin", origin)
	return dir
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p) //nolint:gosec // G304: a path under t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestStartSeedsTheCloneAndStagesNothing(t *testing.T) {
	e := newEnv(t)
	dir := newClone(t, "git@github.com:ada/dojo.git")
	if err := e.run(t, "start", "white/a"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(dir, "AGENTS.md")); got != mentor.AgentsMD() {
		t.Errorf("AGENTS.md = %q", got)
	}
	if got := read(t, filepath.Join(dir, "CLAUDE.md")); got != mentor.ClaudeMD {
		t.Errorf("CLAUDE.md = %q", got)
	}
	if staged := gitIn(t, dir, "diff", "--cached", "--name-only"); staged != "" {
		t.Errorf("staged: %q", staged)
	}
	if !strings.Contains(e.out.String(), "AGENTS.md") {
		t.Errorf("start did not mention the files:\n%s", e.out)
	}
	// A second start changes nothing and says nothing about the clone.
	if err := e.run(t, "start", "white/a"); err != nil || strings.Contains(e.out.String(), "added to") {
		t.Errorf("second start: %v\n%s", err, e.out)
	}
}

func TestStartWritesNothingWhenEitherFileExists(t *testing.T) {
	for _, have := range []string{"CLAUDE.md", "AGENTS.md"} {
		e := newEnv(t)
		dir := newClone(t, "git@github.com:ada/dojo.git")
		if err := os.WriteFile(filepath.Join(dir, have), []byte("mine\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := e.run(t, "start", "white/a"); err != nil {
			t.Fatal(err)
		}
		for _, f := range []string{"AGENTS.md", "CLAUDE.md"} {
			_, err := os.Stat(filepath.Join(dir, f))
			if f == have {
				if got := read(t, filepath.Join(dir, f)); got != "mine\n" {
					t.Errorf("%s overwritten: %q", f, got)
				}
			} else if err == nil {
				t.Errorf("%s was written although %s exists", f, have)
			}
		}
	}
}

func TestStartLeavesOtherReposAlone(t *testing.T) {
	e := newEnv(t)
	dir := newClone(t, "git@github.com:ada/notes.git")
	if err := e.run(t, "start", "white/a"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); err == nil {
		t.Error("wrote into a repo that is not the practice repo")
	}
	// No clone at all is fine too.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := e.run(t, "start", "white/a"); err != nil {
		t.Fatal(err)
	}
}

// apiMCP serves GET /v1/tickets/white/a with the given hints and checks the bearer.
func apiMCP(t *testing.T, status int, body string) *env {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tickets/white/a" || r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	e := newEnv(t)
	e.app.Login = LoginConfig{APIURL: srv.URL}
	e.app.Getenv = func(string) string { return "" }
	if err := state.SaveToken("tok"); err != nil {
		t.Fatal(err)
	}
	st, _ := state.Load()
	st.Current, st.HintsShown = "white/a", 2 // the local counter must be ignored
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestMCPHintsOpenedReadsTheAPI(t *testing.T) {
	e := apiMCP(t, 200, `{"hint_count":3,"hints":[{"n":2,"text":"web two"}]}`)
	got := mcpCall(t, e, "hints_opened")
	if got != "Hint 2/3: web two\n" {
		t.Errorf("hints_opened = %q", got)
	}
	e = apiMCP(t, 200, `{"hint_count":3,"hints":[]}`)
	if got := mcpCall(t, e, "hints_opened"); !strings.Contains(got, "not opened any hint") {
		t.Errorf("no hints: %q", got)
	}
}

func mcpError(t *testing.T, e *env, tool string) string {
	t.Helper()
	e.app.In = strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + tool + `"}}` + "\n")
	if err := e.run(t, "mcp"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.out.String(), `"isError":true`) {
		t.Fatalf("want isError: %s", e.out)
	}
	return e.out.String()
}

func TestMCPHintsOpenedFailuresSayTheyCannotRead(t *testing.T) {
	e := apiMCP(t, 500, `{"error":"server_error"}`)
	if got := mcpError(t, e, "hints_opened"); !strings.Contains(got, "cannot read the opened hints") || !strings.Contains(got, "500") {
		t.Errorf("api error: %s", got)
	}
	e = apiMCP(t, 200, `{}`)
	if err := state.RemoveToken(); err != nil {
		t.Fatal(err)
	}
	if got := mcpError(t, e, "hints_opened"); !strings.Contains(got, "dojo login") {
		t.Errorf("logged out: %s", got)
	}
	e = apiMCP(t, 200, `{}`)
	e.app.Login = LoginConfig{APIURL: "http://127.0.0.1:1"}
	if got := mcpError(t, e, "hints_opened"); !strings.Contains(got, "not reachable") {
		t.Errorf("offline: %s", got)
	}
}

func TestMCPHintsOpenedWithKatasKeepsTheLocalCounter(t *testing.T) {
	e := apiMCP(t, 500, `{}`) // an API that would fail if it were asked
	e.app.Getenv = func(k string) string {
		if k == "DOJO_KATAS" {
			return "/some/dir"
		}
		return ""
	}
	if got := mcpCall(t, e, "hints_opened"); !strings.Contains(got, "Hint 1/2: one") || !strings.Contains(got, "Hint 2/2") {
		t.Errorf("hints_opened = %q", got)
	}
}
