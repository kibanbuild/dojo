package doctor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kibanbuild/dojo/internal/ghread"
)

const (
	pubA = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA me@laptop"
	pubB = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB other"
)

type ghServer struct {
	routes map[string]func(w http.ResponseWriter)
	calls  atomic.Int32
	srv    *httptest.Server
}

func jsonBody(body string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) { _, _ = w.Write([]byte(body)) }
}

func status(code int) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) { w.WriteHeader(code) }
}

func newGH(t *testing.T, routes map[string]func(http.ResponseWriter)) (*ghread.Client, *ghServer) {
	t.Helper()
	g := &ghServer{routes: routes}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.calls.Add(1)
		if h, ok := g.routes[r.URL.Path]; ok {
			h(w)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(g.srv.Close)
	c, err := ghread.New(ghread.Config{BaseURL: g.srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	return c, g
}

func happyRoutes() map[string]func(http.ResponseWriter) {
	return map[string]func(http.ResponseWriter){
		"/users/ada/ssh_signing_keys": jsonBody(`[{"id":1,"key":"` + pubA + `"}]`),
		"/users/ada/keys":             jsonBody(`[{"id":2,"key":"` + pubA + `"}]`),
		"/repos/ada/dojo":             jsonBody(`{"name":"dojo","private":false}`),
		"/repos/ada/dojo/rules/branches/main": jsonBody(`[{"type":"required_signatures","ruleset_id":1},` +
			`{"type":"non_fast_forward","ruleset_id":1},{"type":"deletion","ruleset_id":1}]`),
		"/repos/ada/dojo/contents/AGENTS.md": jsonBody(`{"type":"file","name":"AGENTS.md"}`),
	}
}

var ada = Account{Login: "ada", ID: 1001}

func sshMachine() Machine {
	return Machine{SigningPub: pubA, CloneOverSSH: true, SSHOfferedKeys: []string{pubA}, GitEmail: "1001+ada@users.noreply.github.com"}
}

func byID(rs []Result) map[string]Result {
	m := map[string]Result{}
	for _, r := range rs {
		m[r.ID] = r
	}
	return m
}

func TestGitHubChecksAllDone(t *testing.T) {
	c, g := newGH(t, happyRoutes())
	rs := EvaluateGitHub(context.Background(), sshMachine(), ada, c)
	got := byID(rs)
	for _, id := range []string{"git_email_github", "key_signing", "key_auth", "repo", "ruleset", "agents_md"} {
		if got[id].Status != OK {
			t.Errorf("%s = %+v", id, got[id])
		}
	}
	if len(rs) != 6 {
		t.Errorf("results = %d", len(rs))
	}
	if n := g.calls.Load(); n != 5 {
		t.Errorf("calls = %d, want 5 (within the budget of 7)", n)
	}
	groups := map[string]Group{"git_email_github": GroupAdvice, "key_signing": GroupKey, "key_auth": GroupKey, "repo": GroupRepo, "ruleset": GroupRepo, "agents_md": GroupRepo}
	for id, g := range groups {
		if got[id].Group != g {
			t.Errorf("%s group = %q", id, got[id].Group)
		}
	}
}

func TestGitHubChecksNothingDone(t *testing.T) {
	routes := happyRoutes()
	routes["/users/ada/ssh_signing_keys"] = jsonBody(`[{"id":1,"key":"` + pubB + `"}]`)
	routes["/users/ada/keys"] = jsonBody(`[]`)
	delete(routes, "/repos/ada/dojo")
	c, g := newGH(t, routes)
	m := sshMachine()
	m.GitEmail = "ada@example.test"
	got := byID(EvaluateGitHub(context.Background(), m, ada, c))
	for id, want := range map[string]Status{"git_email_github": Warn, "key_signing": Fail, "key_auth": Fail, "repo": Fail, "ruleset": Fail, "agents_md": Fail} {
		if got[id].Status != want {
			t.Errorf("%s = %+v, want %v", id, got[id], want)
		}
	}
	if !strings.Contains(got["repo"].Fix, "create it, public") {
		t.Errorf("repo fix = %q", got["repo"].Fix)
	}
	if n := g.calls.Load(); n != 3 {
		t.Errorf("calls = %d: a missing repo must not cost the rules and contents reads", n)
	}
}

func TestGitHubRepoPrivateOrMissingSaysCreateItPublic(t *testing.T) {
	routes := happyRoutes()
	routes["/repos/ada/dojo"] = jsonBody(`{"name":"dojo","private":true}`)
	c, _ := newGH(t, routes)
	got := byID(EvaluateGitHub(context.Background(), sshMachine(), ada, c))
	if got["repo"].Status != Fail || !strings.Contains(got["repo"].Text, "private") || !strings.Contains(got["repo"].Fix, "create it, public") {
		t.Errorf("repo = %+v", got["repo"])
	}
}

func TestGitHubRulesetNamesMissingRules(t *testing.T) {
	tests := []struct {
		name, rules string
		want        Status
		missing     []string
	}{
		{"all three", `[{"type":"required_signatures"},{"type":"non_fast_forward"},{"type":"deletion"},{"type":"pull_request"}]`, OK, nil},
		{"none", `[]`, Fail, []string{"required_signatures", "non_fast_forward", "deletion"}},
		{"no signatures", `[{"type":"non_fast_forward"},{"type":"deletion"}]`, Fail, []string{"required_signatures"}},
		{"no deletion", `[{"type":"required_signatures"},{"type":"non_fast_forward"}]`, Fail, []string{"deletion"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			routes := happyRoutes()
			routes["/repos/ada/dojo/rules/branches/main"] = jsonBody(tt.rules)
			c, _ := newGH(t, routes)
			r := byID(EvaluateGitHub(context.Background(), sshMachine(), ada, c))["ruleset"]
			if r.Status != tt.want {
				t.Fatalf("ruleset = %+v", r)
			}
			for _, m := range tt.missing {
				if !strings.Contains(r.Text, m) {
					t.Errorf("text %q lacks %s", r.Text, m)
				}
			}
			if tt.want == Fail && (!strings.Contains(r.Fix, "Require signed commits") || !strings.Contains(r.Fix, "main")) {
				t.Errorf("fix = %q", r.Fix)
			}
		})
	}
}

func TestGitHubAgentsMD(t *testing.T) {
	tests := []struct {
		name  string
		route func(http.ResponseWriter)
		want  Status
	}{
		{"missing", status(404), Fail},
		{"directory answers an array", jsonBody(`[{"type":"file","name":"AGENTS.md"}]`), Unknown},
		{"a directory object", jsonBody(`{"type":"dir"}`), Fail},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			routes := happyRoutes()
			routes["/repos/ada/dojo/contents/AGENTS.md"] = tt.route
			c, _ := newGH(t, routes)
			if r := byID(EvaluateGitHub(context.Background(), sshMachine(), ada, c))["agents_md"]; r.Status != tt.want {
				t.Errorf("agents_md = %+v", r)
			}
		})
	}
}

func TestGitHubKeyAuthNotNeededOverHTTPS(t *testing.T) {
	c, g := newGH(t, happyRoutes())
	m := sshMachine()
	m.CloneOverSSH = false
	r := byID(EvaluateGitHub(context.Background(), m, ada, c))["key_auth"]
	if r.Status != OK || r.Text != "Not needed: your clone uses HTTPS" {
		t.Errorf("key_auth = %+v", r)
	}
	if n := g.calls.Load(); n != 4 {
		t.Errorf("calls = %d, want 4", n)
	}
}

func TestGitHubKeyChecksWithoutAKey(t *testing.T) {
	c, g := newGH(t, happyRoutes())
	m := sshMachine()
	m.SigningPub, m.SSHOfferedKeys = "", nil
	got := byID(EvaluateGitHub(context.Background(), m, ada, c))
	if got["key_signing"].Status != Fail || got["key_auth"].Status != Fail {
		t.Errorf("results = %+v", got)
	}
	if n := g.calls.Load(); n != 3 {
		t.Errorf("calls = %d, want 3 (repo, rules, contents)", n)
	}
}

func TestGitHubNetworkFailureIsUnknownNeverFail(t *testing.T) {
	c, g := newGH(t, happyRoutes())
	g.srv.Close()
	rs := EvaluateGitHub(context.Background(), sshMachine(), ada, c)
	for _, r := range rs {
		if r.ID == "git_email_github" {
			continue
		}
		if r.Status != Unknown || r.Text != "GitHub is unreachable" {
			t.Errorf("%s = %+v", r.ID, r)
		}
	}
	if len(rs) != 6 {
		t.Errorf("results = %d", len(rs))
	}
}

func TestGitHubRateLimitIsUnknownWithMinutes(t *testing.T) {
	reset := strconv.FormatInt(time.Now().Add(17*time.Minute).Unix(), 10)
	limited := func(w http.ResponseWriter) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", reset)
		w.WriteHeader(http.StatusForbidden)
	}
	routes := happyRoutes()
	routes["/users/ada/ssh_signing_keys"] = limited
	c, g := newGH(t, routes)
	got := byID(EvaluateGitHub(context.Background(), sshMachine(), ada, c))
	for _, id := range []string{"key_signing", "key_auth", "repo", "ruleset", "agents_md"} {
		if got[id].Status != Unknown || !strings.HasPrefix(got[id].Text, "GitHub is limiting requests, try again in 1") {
			t.Errorf("%s = %+v", id, got[id])
		}
	}
	if n := g.calls.Load(); n != 1 {
		t.Errorf("calls = %d: once limited, no more requests", n)
	}
}

func TestGitHubForbiddenWithBudgetLeftIsNotARateLimit(t *testing.T) {
	routes := happyRoutes()
	routes["/repos/ada/dojo"] = status(403)
	c, _ := newGH(t, routes)
	got := byID(EvaluateGitHub(context.Background(), sshMachine(), ada, c))
	if got["repo"].Status != Unknown || got["ruleset"].Status != Unknown || got["key_signing"].Status != OK {
		t.Errorf("results = %+v", got)
	}
}

func TestGitHubEmailAdvisory(t *testing.T) {
	for email, want := range map[string]Status{
		"1001+ada@users.noreply.github.com": OK,
		"Ada@users.noreply.github.com":      OK,
		"ada@example.test":                  Warn,
		"":                                  Warn,
	} {
		m := sshMachine()
		m.GitEmail = email
		c, _ := newGH(t, happyRoutes())
		r := byID(EvaluateGitHub(context.Background(), m, ada, c))["git_email_github"]
		if r.Status != want || strings.Contains(r.Text, "@") {
			t.Errorf("%q = %+v", email, r)
		}
	}
}

func TestGitHubUnknownTextsAreReadable(t *testing.T) {
	routes := happyRoutes()
	delete(routes, "/users/ada/ssh_signing_keys") // a 404 on the account lookups
	delete(routes, "/users/ada/keys")
	c, _ := newGH(t, routes)
	got := byID(EvaluateGitHub(context.Background(), sshMachine(), ada, c))
	for _, id := range []string{"key_signing", "key_auth"} {
		if got[id].Status != Unknown || got[id].Text != "GitHub could not find the account ada" {
			t.Errorf("%s = %+v", id, got[id])
		}
	}

	c, _ = newGH(t, happyRoutes())
	bad := Account{Login: "not_a_login", ID: 5}
	for id, r := range byID(EvaluateGitHub(context.Background(), sshMachine(), bad, c)) {
		if id != "git_email_github" && (r.Status != Unknown || strings.Contains(r.Text, "invalid argument") || !strings.Contains(r.Text, "not valid")) {
			t.Errorf("%s = %+v", id, r)
		}
	}

	routes = happyRoutes()
	routes["/repos/ada/dojo/rules/branches/main"] = status(404)
	c, _ = newGH(t, routes)
	if r := byID(EvaluateGitHub(context.Background(), sshMachine(), ada, c))["ruleset"]; r.Status != Unknown || r.Text != "GitHub could not find the rules of main in ada/dojo" {
		t.Errorf("ruleset = %+v", r)
	}
}

func TestGitHubKeySigningDistinguishesAnUnreadableKey(t *testing.T) {
	c, g := newGH(t, happyRoutes())
	m := sshMachine()
	m.SigningPub, m.GitSigningKey = "", "~/.ssh/gone.pub"
	r := byID(EvaluateGitHub(context.Background(), m, ada, c))["key_signing"]
	if r.Status != Fail || !strings.Contains(r.Text, "user.signingkey") || strings.Contains(r.Fix, "create an SSH key") {
		t.Errorf("unreadable = %+v", r)
	}
	if n := g.calls.Load(); n != 4 {
		t.Errorf("calls = %d, want 4", n)
	}
	m.GitSigningKey = ""
	r = byID(EvaluateGitHub(context.Background(), m, ada, c))["key_signing"]
	if r.Status != Fail || !strings.Contains(r.Fix, "create an SSH key") {
		t.Errorf("no key = %+v", r)
	}
}

func TestSigningPubSources(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	if got := signingPub("", pubA); got != pubA {
		t.Errorf("default = %q", got)
	}
	if got := signingPub("key::"+pubB, pubA); got != pubB {
		t.Errorf("literal = %q", got)
	}
	if got := signingPub(dir+"/nothing", pubA); got != "" {
		t.Errorf("missing file = %q", got)
	}
}

func TestIsSSHURL(t *testing.T) {
	for u, want := range map[string]bool{
		"git@github.com:ada/dojo.git":       true,
		"git@github.com:":                   true,
		"ssh://git@github.com/ada/dojo.git": true,
		"https://github.com/ada/dojo.git":   false,
		"file:///srv/git/ada/dojo.git":      false,
		"/srv/git/ada/dojo.git":             false,
	} {
		if isSSHURL(u) != want {
			t.Errorf("isSSHURL(%q) = %v", u, !want)
		}
	}
}
