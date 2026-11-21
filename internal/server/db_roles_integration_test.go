//go:build integration

package server_test

import (
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

// Connects by the container address: the image trusts loopback, other addresses use scram.
func TestServiceRoles(t *testing.T) {
	roles := map[string]string{
		"dojo_api":   envExample(t, "DOJO_API_DB_PASSWORD"),
		"dojo_admin": envExample(t, "DOJO_ADMIN_DB_PASSWORD"),
	}
	for role, pw := range roles {
		t.Run(role, func(t *testing.T) {
			if out, err := psqlAs(t, role, "wrong-"+pw, "SELECT 1"); err == nil {
				t.Fatalf("login with a wrong password succeeded: %s", out)
			}
			if out, err := psqlAs(t, role, pw, "SELECT count(*) FROM orgs"); err != nil {
				t.Fatalf("select orgs: %v %q", err, out)
			}
			for name, sql := range map[string]string{
				"create table": "CREATE TABLE should_fail (id int)",
				"insert orgs":  "INSERT INTO orgs (slug, name) VALUES ('x', 'X')",
			} {
				out, err := psqlAs(t, role, pw, sql)
				if err == nil || !strings.Contains(out, "permission denied") {
					t.Fatalf("%s: want permission denied, got %v %q", name, err, out)
				}
			}
		})
	}
}
