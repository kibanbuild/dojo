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
	{"dojo_api", "INSERT INTO learners (org_id, github_id, github_login, username, language, age_confirmed_at, terms_accepted_at, terms_version, usage_stats) SELECT id, 1, 'a', 'abc', 'en', now(), now(), '2026-11-20', true FROM orgs WHERE false", true},
	{"dojo_api", "UPDATE learners SET language = 'es', usage_stats = false, analytics_consent = true, email = 'a@b.c', username = 'abcd', github_login = 'x' WHERE false", true},
	{"dojo_api", "INSERT INTO sessions (org_id, learner_id, token_hash) SELECT org_id, id, '\\x00'::bytea FROM learners WHERE false", true},
	{"dojo_api", "UPDATE sessions SET last_used_at = now(), revoked_at = now(), updated_at = now() WHERE false", true},
	{"dojo_api", "UPDATE learners SET suspended_at = now() WHERE false", false},
	{"dojo_api", "UPDATE learners SET github_id = 2 WHERE false", false},
	{"dojo_api", "UPDATE learners SET org_id = org_id WHERE false", false},
	{"dojo_api", "UPDATE learners SET age_confirmed_at = now() WHERE false", false},
	{"dojo_api", "UPDATE learners SET terms_accepted_at = now() WHERE false", false},
	{"dojo_api", "INSERT INTO learners (org_id, github_id, github_login, username, age_confirmed_at, terms_accepted_at, suspended_at) SELECT id, 1, 'a', 'abc', now(), now(), now() FROM orgs WHERE false", false},
	{"dojo_api", "UPDATE learners SET terms_version = 'x' WHERE false", false},
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
	{"dojo_admin", "UPDATE learners SET terms_version = 'x' WHERE false", false},
	{"dojo_admin", "INSERT INTO learners (org_id, github_id, github_login, username, age_confirmed_at, terms_accepted_at, terms_version) SELECT id, 1, 'a', 'abc', now(), now(), 'x' FROM orgs WHERE false", false},
	{"dojo_admin", "DELETE FROM learners WHERE false", false},
	{"dojo_admin", "SELECT token_hash FROM sessions", false},
	{"dojo_admin", "SELECT count(*) FROM sessions", false},
	{"dojo_admin", "UPDATE sessions SET revoked_at = now() WHERE false", false},

	// cli_tokens: the API reads, inserts the device row and touches last_used_at and revoked_at;
	// the hash and the device fields never change; nothing is deleted; the operator service has no access.
	{"dojo_api", "SELECT token_hash, device_name, os, cli_version, last_used_at, revoked_at FROM cli_tokens", true},
	{"dojo_api", "INSERT INTO cli_tokens (org_id, learner_id, token_hash, device_name, os, cli_version) SELECT org_id, id, '\\x00'::bytea, 'd', 'linux', '1' FROM learners WHERE false", true},
	{"dojo_api", "UPDATE cli_tokens SET last_used_at = now(), revoked_at = now(), updated_at = now() WHERE false", true},
	{"dojo_api", "INSERT INTO cli_tokens (org_id, learner_id, token_hash, device_name, os, cli_version, revoked_at) SELECT org_id, id, '\\x00'::bytea, 'd', 'linux', '1', now() FROM learners WHERE false", false},
	{"dojo_api", "INSERT INTO cli_tokens (org_id, learner_id, token_hash, device_name, os, cli_version, last_used_at) SELECT org_id, id, '\\x00'::bytea, 'd', 'linux', '1', now() FROM learners WHERE false", false},
	{"dojo_api", "UPDATE cli_tokens SET token_hash = '\\x00' WHERE false", false},
	{"dojo_api", "UPDATE cli_tokens SET learner_id = learner_id WHERE false", false},
	{"dojo_api", "UPDATE cli_tokens SET org_id = org_id WHERE false", false},
	{"dojo_api", "UPDATE cli_tokens SET device_name = 'x' WHERE false", false},
	{"dojo_api", "UPDATE cli_tokens SET os = 'x' WHERE false", false},
	{"dojo_api", "UPDATE cli_tokens SET cli_version = 'x' WHERE false", false},
	{"dojo_api", "UPDATE cli_tokens SET created_at = now() WHERE false", false},
	{"dojo_api", "DELETE FROM cli_tokens WHERE false", false},
	{"dojo_admin", "SELECT count(*) FROM cli_tokens", false},
	{"dojo_admin", "SELECT token_hash FROM cli_tokens", false},
	{"dojo_admin", "UPDATE cli_tokens SET revoked_at = now() WHERE false", false},
	{"dojo_admin", "INSERT INTO cli_tokens (org_id, learner_id, token_hash, device_name, os, cli_version) SELECT org_id, id, '\\x00'::bytea, 'd', 'linux', '1' FROM learners WHERE false", false},
	{"dojo_admin", "DELETE FROM cli_tokens WHERE false", false},

	// telemetry_events: the API only inserts (no SELECT, UPDATE or DELETE; rows go by cascade or
	// prune_telemetry() as owner); the operator service has no access. `at` and `id` keep defaults.
	{"dojo_api", "INSERT INTO telemetry_events (org_id, learner_id, command, result, duration_ms, cli_version, os) SELECT id, NULL, 'check', 'ok', 1, 'v1', 'linux' FROM orgs WHERE false", true},
	{"dojo_api", "INSERT INTO telemetry_events (org_id, command, result, duration_ms, cli_version, os) SELECT id, 'check', 'ok', 1, 'v1', 'linux' FROM orgs WHERE false", true},
	{"dojo_api", "INSERT INTO telemetry_events (org_id, command, result, duration_ms, cli_version, os, at) SELECT id, 'check', 'ok', 1, 'v1', 'linux', now() FROM orgs WHERE false", false},
	{"dojo_api", "INSERT INTO telemetry_events (id, org_id, command, result, duration_ms, cli_version, os) SELECT gen_random_uuid(), id, 'check', 'ok', 1, 'v1', 'linux' FROM orgs WHERE false", false},
	{"dojo_api", "SELECT count(*) FROM telemetry_events", false},
	{"dojo_api", "SELECT learner_id FROM telemetry_events", false},
	{"dojo_api", "UPDATE telemetry_events SET result = 'x' WHERE false", false},
	{"dojo_api", "UPDATE telemetry_events SET learner_id = NULL WHERE false", false},
	{"dojo_api", "DELETE FROM telemetry_events WHERE false", false},
	{"dojo_admin", "SELECT count(*) FROM telemetry_events", false},
	{"dojo_admin", "INSERT INTO telemetry_events (org_id, command, result, duration_ms, cli_version, os) SELECT id, 'check', 'ok', 1, 'v1', 'linux' FROM orgs WHERE false", false},
	{"dojo_admin", "UPDATE telemetry_events SET result = 'x' WHERE false", false},
	{"dojo_admin", "DELETE FROM telemetry_events WHERE false", false},
	// prune_telemetry() is owner-only: no service role executes it, so none gains DELETE through it.
	{"dojo_api", "SELECT prune_telemetry()", false},
	{"dojo_admin", "SELECT prune_telemetry()", false},

	// operators, admin_sessions, admin_audit: dojo_api has no grant at all. dojo_admin writes
	// column-level (revoked_at is the only update) and can never delete; the audit is append-only.
	{"dojo_api", "SELECT count(*) FROM operators", false},
	{"dojo_api", "INSERT INTO operators (github_id, github_login) SELECT 1, 'a' WHERE false", false},
	{"dojo_api", "UPDATE operators SET revoked_at = now() WHERE false", false},
	{"dojo_api", "DELETE FROM operators WHERE false", false},
	{"dojo_api", "SELECT count(*) FROM admin_sessions", false},
	{"dojo_api", "SELECT token_hash FROM admin_sessions", false},
	{"dojo_api", "INSERT INTO admin_sessions (operator_id, token_hash) SELECT id, '\\x00'::bytea FROM operators WHERE false", false},
	{"dojo_api", "UPDATE admin_sessions SET revoked_at = now() WHERE false", false},
	{"dojo_api", "DELETE FROM admin_sessions WHERE false", false},
	{"dojo_api", "SELECT count(*) FROM admin_audit", false},
	{"dojo_api", "INSERT INTO admin_audit (operator_id, operator_login, action, target_type, target_id) SELECT id, 'a', 'x', 'y', 'z' FROM operators WHERE false", false},
	{"dojo_api", "UPDATE admin_audit SET reason = 'x' WHERE false", false},
	{"dojo_api", "DELETE FROM admin_audit WHERE false", false},
	{"dojo_admin", "SELECT github_id, github_login, added_by, added_at, revoked_at FROM operators", true},
	{"dojo_admin", "INSERT INTO operators (github_id, github_login, added_by) SELECT 1, 'a', id FROM operators WHERE false", true},
	{"dojo_admin", "UPDATE operators SET revoked_at = now() WHERE false", true},
	{"dojo_admin", "UPDATE operators SET github_id = 2 WHERE false", false},
	{"dojo_admin", "UPDATE operators SET github_login = 'x' WHERE false", false},
	{"dojo_admin", "UPDATE operators SET added_by = NULL WHERE false", false},
	{"dojo_admin", "UPDATE operators SET added_at = now() WHERE false", false},
	{"dojo_admin", "INSERT INTO operators (github_id, github_login, revoked_at) SELECT 1, 'a', now() WHERE false", false},
	{"dojo_admin", "INSERT INTO operators (id, github_id, github_login) SELECT gen_random_uuid(), 1, 'a' WHERE false", false},
	{"dojo_admin", "DELETE FROM operators WHERE false", false},
	{"dojo_admin", "SELECT token_hash, created_at, expires_at, revoked_at FROM admin_sessions", true},
	{"dojo_admin", "INSERT INTO admin_sessions (operator_id, token_hash) SELECT id, '\\x00'::bytea FROM operators WHERE false", true},
	{"dojo_admin", "UPDATE admin_sessions SET revoked_at = now() WHERE false", true},
	{"dojo_admin", "UPDATE admin_sessions SET expires_at = now() WHERE false", false},
	{"dojo_admin", "UPDATE admin_sessions SET token_hash = '\\x00' WHERE false", false},
	{"dojo_admin", "UPDATE admin_sessions SET operator_id = operator_id WHERE false", false},
	{"dojo_admin", "INSERT INTO admin_sessions (operator_id, token_hash, expires_at) SELECT id, '\\x00'::bytea, now() FROM operators WHERE false", false},
	{"dojo_admin", "DELETE FROM admin_sessions WHERE false", false},
	{"dojo_admin", "SELECT operator_login, action, target_type, target_id, reason, before, after, at FROM admin_audit", true},
	{"dojo_admin", "INSERT INTO admin_audit (operator_id, operator_login, action, target_type, target_id, reason, before, after) SELECT id, 'a', 'x', 'y', 'z', 'r', '{}'::jsonb, '{}'::jsonb FROM operators WHERE false", true},
	{"dojo_admin", "INSERT INTO admin_audit (operator_id, operator_login, action, target_type, target_id, at) SELECT id, 'a', 'x', 'y', 'z', now() FROM operators WHERE false", false},
	{"dojo_admin", "UPDATE admin_audit SET reason = 'x' WHERE false", false},
	{"dojo_admin", "UPDATE admin_audit SET before = NULL, after = NULL WHERE false", false},
	{"dojo_admin", "DELETE FROM admin_audit WHERE false", false},
	{"dojo_admin", "INSERT INTO admin_audit (operator_id, operator_login, action, target_type, target_id) SELECT id, 'a', 'x', 'y', 'z' FROM operators WHERE false ON CONFLICT (id) DO UPDATE SET reason = 'x'", false},
	{"dojo_admin", "TRUNCATE admin_audit", false},
	{"dojo_admin", "TRUNCATE operators CASCADE", false},
	{"dojo_admin", "TRUNCATE admin_sessions", false},
	{"dojo_api", "SELECT prune_admin_audit()", false},
	{"dojo_admin", "SELECT prune_admin_audit()", false},

	// email_outbox: both service roles select and insert the queueing columns; id, sent_at and
	// created_at keep defaults; no UPDATE (mark_email_sent() lands in M3) and no DELETE.
	{"dojo_api", "SELECT id, learner_id, to_address, kind, ref, sent_at, created_at FROM email_outbox", true},
	{"dojo_api", "INSERT INTO email_outbox (learner_id, to_address, kind, ref) SELECT NULL, 'a@b.c', 'E1', 'x' WHERE false", true},
	{"dojo_api", "INSERT INTO email_outbox (learner_id, to_address, kind, ref, sent_at) SELECT NULL, 'a@b.c', 'E1', 'x', now() WHERE false", false},
	{"dojo_api", "INSERT INTO email_outbox (id, kind, ref) SELECT gen_random_uuid(), 'E1', 'x' WHERE false", false},
	{"dojo_api", "UPDATE email_outbox SET sent_at = now() WHERE false", false},
	{"dojo_api", "UPDATE email_outbox SET to_address = NULL WHERE false", false},
	{"dojo_api", "DELETE FROM email_outbox WHERE false", false},
	{"dojo_api", "TRUNCATE email_outbox", false},
	{"dojo_admin", "SELECT id, learner_id, to_address, kind, ref, sent_at, created_at FROM email_outbox", true},
	{"dojo_admin", "INSERT INTO email_outbox (learner_id, to_address, kind, ref) SELECT NULL, 'a@b.c', 'E1', 'x' WHERE false", true},
	{"dojo_admin", "INSERT INTO email_outbox (learner_id, to_address, kind, ref, created_at) SELECT NULL, 'a@b.c', 'E1', 'x', now() WHERE false", false},
	{"dojo_admin", "UPDATE email_outbox SET sent_at = now() WHERE false", false},
	{"dojo_admin", "DELETE FROM email_outbox WHERE false", false},
	{"dojo_admin", "TRUNCATE email_outbox", false},
	// delete_learner() is the one way an account goes: executable by both service roles, and an
	// unknown id is a no-op. Its EXECUTE list is also asserted in delete_learner_integration_test.go.
	{"dojo_api", "SELECT delete_learner(gen_random_uuid())", true},
	{"dojo_admin", "SELECT delete_learner(gen_random_uuid())", true},

	// Catalog tables (migration 00012): content written only by the load job as the migration role.
	// dojo_api reads; dojo_admin has no grant until a screen joins them.
	{"dojo_api", "SELECT id, slug, title, status, position FROM paths", true},
	{"dojo_api", "SELECT id, slug, title, kind, has_exam, advanced FROM roles", true},
	{"dojo_api", "SELECT path_id, role_id, position FROM path_roles", true},
	{"dojo_api", "SELECT id, role_id, kind, position, title, est_minutes, tags, brief, hints, awards_mark, catalog_version, bundle_key, bundle_sha256, bundle_bytes FROM catalog_items", true},
	{"dojo_api", "SELECT id, catalog_version FROM catalog_state", true},
	{"dojo_api", "INSERT INTO paths (org_id, slug, title, status, position) SELECT id, 'x', 'X', 'planned', 1 FROM orgs WHERE false", false},
	{"dojo_api", "UPDATE paths SET title = 'x' WHERE false", false},
	{"dojo_api", "DELETE FROM paths WHERE false", false},
	{"dojo_api", "TRUNCATE paths CASCADE", false},
	{"dojo_api", "INSERT INTO roles (org_id, slug, title, kind, has_exam) SELECT id, 'x', 'X', 'role', false FROM orgs WHERE false", false},
	{"dojo_api", "UPDATE roles SET title = 'x' WHERE false", false},
	{"dojo_api", "DELETE FROM roles WHERE false", false},
	{"dojo_api", "TRUNCATE roles CASCADE", false},
	{"dojo_api", "INSERT INTO path_roles (org_id, path_id, role_id, position) SELECT id, gen_random_uuid(), gen_random_uuid(), 1 FROM orgs WHERE false", false},
	{"dojo_api", "UPDATE path_roles SET position = 2 WHERE false", false},
	{"dojo_api", "DELETE FROM path_roles WHERE false", false},
	{"dojo_api", "TRUNCATE path_roles", false},
	{"dojo_api", "INSERT INTO catalog_items (id, org_id, kind, position, title, est_minutes, catalog_version) SELECT 'a/b', id, 'practice', 1, 'X', 1, '000000000000' FROM orgs WHERE false", false},
	{"dojo_api", "UPDATE catalog_items SET title = 'x' WHERE false", false},
	{"dojo_api", "DELETE FROM catalog_items WHERE false", false},
	{"dojo_api", "TRUNCATE catalog_items", false},
	{"dojo_api", "INSERT INTO catalog_state (id, org_id, catalog_version) SELECT 1, id, '000000000000' FROM orgs WHERE false", false},
	{"dojo_api", "UPDATE catalog_state SET catalog_version = '000000000000' WHERE false", false},
	{"dojo_api", "DELETE FROM catalog_state WHERE false", false},
	{"dojo_api", "TRUNCATE catalog_state", false},
	{"dojo_admin", "SELECT count(*) FROM paths", false},
	{"dojo_admin", "SELECT count(*) FROM roles", false},
	{"dojo_admin", "SELECT count(*) FROM path_roles", false},
	{"dojo_admin", "SELECT count(*) FROM catalog_items", false},
	{"dojo_admin", "SELECT count(*) FROM catalog_state", false},
	{"dojo_admin", "INSERT INTO paths (org_id, slug, title, status, position) SELECT id, 'x', 'X', 'planned', 1 FROM orgs WHERE false", false},
	{"dojo_admin", "UPDATE catalog_items SET title = 'x' WHERE false", false},
	{"dojo_admin", "DELETE FROM catalog_state WHERE false", false},
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
