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
