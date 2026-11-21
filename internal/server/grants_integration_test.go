//go:build integration

package server_test

import (
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// psqlAs runs psql inside the db container: Postgres stays unpublished.
func psqlAs(t *testing.T, role, password, sql string) (string, error) {
	t.Helper()
	project := os.Getenv("DOJO_COMPOSE_PROJECT")
	if project == "" {
		t.Fatal("DOJO_COMPOSE_PROJECT is not set: run `make test-integration`")
	}
	ids, err := exec.Command("docker", "ps", "-q",
		"--filter", "label=com.docker.compose.project="+project,
		"--filter", "label=com.docker.compose.service=db").Output()
	id := strings.TrimSpace(string(ids))
	if err != nil || id == "" {
		t.Fatalf("find db container: %v %q", err, ids)
	}
	out, err := exec.Command("docker", "exec", "-e", "PGPASSWORD="+password, id,
		"psql", "-h", id, "-U", role, "-d", "dojo", "-v", "ON_ERROR_STOP=1", "-tA", "-c", sql).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func envExample(t *testing.T, key string) string {
	t.Helper()
	b, err := os.ReadFile("../../.env.example")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, key+"="); ok {
			return v
		}
	}
	t.Fatalf("%s not in .env.example", key)
	return ""
}

// grantCases is the grants harness: every table PR appends its rows here, both the writes a role
// is granted and each one it is refused. A refused case must fail on permission denied (42501).
var grantCases = []struct {
	role    string
	sql     string
	allowed bool
}{
	{"dojo_api", "SELECT count(*) FROM orgs", true},
	{"dojo_api", "CREATE TABLE should_fail (id int)", false},
	{"dojo_api", "INSERT INTO orgs (slug, name) VALUES ('x', 'X')", false},
	{"dojo_admin", "SELECT count(*) FROM orgs", true},
	{"dojo_admin", "CREATE TABLE should_fail (id int)", false},
	{"dojo_admin", "INSERT INTO orgs (slug, name) VALUES ('x', 'X')", false},

	// learners and sessions. WHERE false: privileges are checked, no row is touched.
	{"dojo_api", "SELECT token_hash FROM sessions", true},
	{"dojo_api", "INSERT INTO learners (org_id, github_id, github_login, username, language, age_confirmed_at, terms_accepted_at, usage_stats) SELECT id, 1, 'a', 'abc', 'en', now(), now(), true FROM orgs WHERE false", true},
	{"dojo_api", "UPDATE learners SET language = 'es', usage_stats = false, analytics_consent = true, email = 'a@b.c', username = 'abcd', github_login = 'x' WHERE false", true},
	{"dojo_api", "INSERT INTO sessions (org_id, learner_id, token_hash) SELECT org_id, id, '\\x00'::bytea FROM learners WHERE false", true},
	{"dojo_api", "UPDATE sessions SET last_used_at = now(), revoked_at = now(), updated_at = now() WHERE false", true},
	{"dojo_api", "UPDATE learners SET suspended_at = now() WHERE false", false},
	{"dojo_api", "UPDATE learners SET github_id = 2 WHERE false", false},
	{"dojo_api", "UPDATE learners SET org_id = org_id WHERE false", false},
	{"dojo_api", "UPDATE learners SET age_confirmed_at = now() WHERE false", false},
	{"dojo_api", "UPDATE learners SET terms_accepted_at = now() WHERE false", false},
	{"dojo_api", "INSERT INTO learners (org_id, github_id, github_login, username, age_confirmed_at, terms_accepted_at, suspended_at) SELECT id, 1, 'a', 'abc', now(), now(), now() FROM orgs WHERE false", false},
	{"dojo_api", "DELETE FROM learners WHERE false", false},
	{"dojo_api", "DELETE FROM sessions WHERE false", false},
	{"dojo_api", "UPDATE sessions SET token_hash = '\\x00' WHERE false", false},
	{"dojo_api", "UPDATE sessions SET learner_id = learner_id WHERE false", false},
	{"dojo_admin", "SELECT github_login, email, suspended_at FROM learners", true},
	{"dojo_admin", "UPDATE learners SET suspended_at = now(), updated_at = now() WHERE false", true},
	{"dojo_admin", "UPDATE learners SET email = 'x' WHERE false", false},
	{"dojo_admin", "UPDATE learners SET username = 'x' WHERE false", false},
	{"dojo_admin", "UPDATE learners SET github_id = 2 WHERE false", false},
	{"dojo_admin", "INSERT INTO learners (org_id, github_id, github_login, username, age_confirmed_at, terms_accepted_at) SELECT id, 1, 'a', 'abc', now(), now() FROM orgs WHERE false", false},
	{"dojo_admin", "DELETE FROM learners WHERE false", false},
	{"dojo_admin", "SELECT token_hash FROM sessions", false},
	{"dojo_admin", "SELECT count(*) FROM sessions", false},
	{"dojo_admin", "UPDATE sessions SET revoked_at = now() WHERE false", false},
}

// Connects by the container address: the image trusts loopback, other addresses use scram.
func TestRoleGrants(t *testing.T) {
	passwords := map[string]string{
		"dojo_api":   envExample(t, "DOJO_API_DB_PASSWORD"),
		"dojo_admin": envExample(t, "DOJO_ADMIN_DB_PASSWORD"),
	}
	for role, pw := range passwords {
		t.Run(role+"/wrong password refused", func(t *testing.T) {
			if out, err := psqlAs(t, role, "wrong-"+pw, "SELECT 1"); err == nil {
				t.Fatalf("login with a wrong password succeeded: %s", out)
			}
		})
	}
	for _, c := range grantCases {
		t.Run(c.role+"/"+c.sql, func(t *testing.T) {
			out, err := psqlAs(t, c.role, passwords[c.role], c.sql)
			switch {
			case c.allowed && err != nil:
				t.Fatalf("want allowed, got %v %q", err, out)
			case !c.allowed && (err == nil || !strings.Contains(out, "permission denied")):
				t.Fatalf("want permission denied, got %v %q", err, out)
			}
		})
	}
}

// The running API must connect as dojo_api, not as the migration role.
func TestAPIConnectsAsDojoAPI(t *testing.T) {
	if code, _ := do(t, http.MethodGet, apiURL(t)+"/v1/health"); code != http.StatusOK {
		t.Fatalf("health: %d", code)
	}
	out, err := psqlAs(t, "dojo", envExample(t, "POSTGRES_PASSWORD"),
		"SELECT DISTINCT usename FROM pg_stat_activity WHERE application_name = 'dojo-api'")
	if err != nil || out != "dojo_api" {
		t.Fatalf("api connections by role: %v %q, want only dojo_api", err, out)
	}
}
