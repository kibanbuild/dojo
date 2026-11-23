//go:build integration

package admin_test

import (
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// runBootstrap runs the real one-off service, as `make admin-bootstrap` does, in the integration
// project. The empty port variables match the ones `make test-integration` started the stack with,
// so compose does not recreate a running service.
func runBootstrap(t *testing.T, login string) (string, error) {
	t.Helper()
	cmd := exec.Command("docker", "compose", "-p", os.Getenv("DOJO_COMPOSE_PROJECT"),
		"-f", "compose.yaml", "-f", "compose.it.yaml", "--env-file", ".env.example",
		"--profile", "jobs", "run", "--rm", "-T", "admin-bootstrap", "bootstrap", login)
	cmd.Dir = "../.."
	cmd.Env = append(os.Environ(), "EDGE_PORT=", "FAKE_GITHUB_PORT=", "ADMIN_PORT=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func resetOperators(t *testing.T) {
	t.Helper()
	reset := "DELETE FROM admin_sessions; DELETE FROM admin_audit; DELETE FROM operators"
	psqlAsOwner(t, reset)
	t.Cleanup(func() { psqlAsOwner(t, reset) })
}

func TestBootstrapFirstOperatorThenSignIn(t *testing.T) {
	resetOperators(t)

	out, err := runBootstrap(t, "ada")
	if err != nil {
		t.Fatalf("bootstrap ada: %v\n%s", err, out)
	}
	if !strings.Contains(out, "operator ") || !strings.Contains(out, "bootstrap done") {
		t.Fatalf("output: %s", out)
	}

	if got := psqlAsOwner(t, "SELECT count(*) || ',' || min(github_id) || ',' || min(github_login) || ',' || bool_and(added_by IS NULL AND revoked_at IS NULL) FROM operators"); got != "1,1001,ada,true" {
		t.Fatalf("operators: %s", got)
	}
	// One audit row, written with the operator, naming the new operator, with markers instead of values.
	row := psqlAsOwner(t, "SELECT count(*) || '|' || min(a.action) || '|' || min(a.target_type) || '|' || bool_and(a.target_id = o.id::text AND a.operator_id = o.id AND a.operator_login = 'ada') || '|' || min(a.after::text) || '|' || bool_and(a.before IS NULL) "+
		"FROM admin_audit a JOIN operators o ON o.id = a.operator_id")
	if row != `1|operator.bootstrap|operator|true|{"github_id": "changed", "github_login": "changed"}|true` {
		t.Fatalf("audit: %s", row)
	}
	if strings.Contains(out, "ada") && strings.Contains(strings.ToLower(out), "token") {
		t.Fatalf("output mentions a token: %s", out)
	}

	// The first operator signs in end to end.
	b := newOperatorBrowser(t)
	resp := b.signIn("ada")
	if c := sessionCookie(resp); resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" || c == nil {
		t.Fatalf("callback: %d %q %v", resp.StatusCode, resp.Header.Get("Location"), c)
	}
	if resp, body := b.admin(http.MethodGet, "/"); resp.StatusCode != 200 || !strings.Contains(body, "Signed in as ada") {
		t.Fatalf("landing: %d %s", resp.StatusCode, body)
	}
}

func TestBootstrapRefusedOnceAnOperatorExists(t *testing.T) {
	resetOperators(t)
	if out, err := runBootstrap(t, "ada"); err != nil {
		t.Fatalf("first bootstrap: %v\n%s", err, out)
	}

	out, err := runBootstrap(t, "bob")
	if err == nil || !strings.Contains(out, "an active operator already exists") {
		t.Fatalf("second bootstrap: %v\n%s", err, out)
	}
	if got := psqlAsOwner(t, "SELECT (SELECT count(*) FROM operators) || ',' || (SELECT count(*) FROM admin_audit)"); got != "1,1" {
		t.Fatalf("rows after the refusal (operators,audit): %s", got)
	}
}

func TestBootstrapRefusals(t *testing.T) {
	resetOperators(t)
	for _, tt := range []struct{ login, want string }{
		{"nobody", "no such github user"},
		{"-bad-", "not a valid GitHub login"},
	} {
		out, err := runBootstrap(t, tt.login)
		if err == nil || !strings.Contains(out, tt.want) {
			t.Errorf("%s: %v\n%s", tt.login, err, out)
		}
	}
	if got := psqlAsOwner(t, "SELECT (SELECT count(*) FROM operators) || ',' || (SELECT count(*) FROM admin_audit)"); got != "0,0" {
		t.Fatalf("rows after refusals: %s", got)
	}
}

func TestBootstrapConcurrentRunsCreateOneOperator(t *testing.T) {
	resetOperators(t)
	type result struct {
		out string
		err error
	}
	res := make(chan result, 2)
	start := make(chan struct{})
	for _, login := range []string{"ada", "cy"} {
		go func() {
			<-start
			out, err := runBootstrap(t, login)
			res <- result{out, err}
		}()
	}
	close(start)
	var ok, refused int
	for range 2 {
		r := <-res
		switch {
		case r.err == nil:
			ok++
		case strings.Contains(r.out, "an active operator already exists"):
			refused++
		default:
			t.Errorf("unexpected failure: %v\n%s", r.err, r.out)
		}
	}
	if ok != 1 || refused != 1 {
		t.Fatalf("succeeded %d, refused %d, want 1 and 1", ok, refused)
	}
	if got := psqlAsOwner(t, "SELECT (SELECT count(*) FROM operators) || ',' || (SELECT count(*) FROM admin_audit)"); got != "1,1" {
		t.Fatalf("rows (operators,audit): %s", got)
	}
}

// Only revoked operators left (the sole operator was revoked): bootstrap of another account is the
// recovery path and succeeds (design 3.7).
func TestBootstrapRecoversWhenOnlyARevokedOperatorExists(t *testing.T) {
	resetOperators(t)
	psqlAsOwner(t, "INSERT INTO operators (github_id, github_login, revoked_at) VALUES (1001, 'ada', now())")

	if out, err := runBootstrap(t, "cy"); err != nil {
		t.Fatalf("bootstrap cy: %v\n%s", err, out)
	}
	if got := psqlAsOwner(t, "SELECT (SELECT count(*) FROM operators) || ',' || (SELECT count(*) FROM operators WHERE revoked_at IS NULL AND github_login = 'cy') || ',' || (SELECT count(*) FROM admin_audit)"); got != "2,1,1" {
		t.Fatalf("rows (operators,active cy,audit): %s", got)
	}
}

// A revoked account cannot be bootstrapped back: revocation is one-way.
func TestBootstrapOfTheRevokedAccountIsRefused(t *testing.T) {
	resetOperators(t)
	psqlAsOwner(t, "INSERT INTO operators (github_id, github_login, revoked_at) VALUES (1001, 'ada', now())")

	out, err := runBootstrap(t, "ada")
	if err == nil || !strings.Contains(out, "revocation is one-way") {
		t.Fatalf("bootstrap of the revoked account: %v\n%s\noperators: %s", err, out,
			psqlAsOwner(t, "SELECT string_agg(github_login || ':' || coalesce(revoked_at::text, 'active'), ',') FROM operators"))
	}
	if got := psqlAsOwner(t, "SELECT (SELECT count(*) FROM operators WHERE revoked_at IS NULL) || ',' || (SELECT count(*) FROM operators) || ',' || (SELECT count(*) FROM admin_audit)"); got != "0,1,0" {
		t.Fatalf("rows (active,operators,audit): %s", got)
	}
}
