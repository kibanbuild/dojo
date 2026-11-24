//go:build integration

package catalogload_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const seedLearner = "INSERT INTO learners (org_id, github_id, github_login, username, age_confirmed_at, terms_accepted_at, terms_version) " +
	"SELECT id, 9101, 'cl', 'cl-9101', now(), now(), 't1' FROM orgs WHERE slug = 'public'"

func removeTicket(t *testing.T, dir string) {
	t.Helper()
	if err := os.RemoveAll(filepath.Join(dir, "support-l1", "service-02")); err != nil {
		t.Fatal(err)
	}
}

func TestRemovalRefusedWhenOnlyHintOpensExist(t *testing.T) {
	wipe(t)
	dir := fixture(t)
	mustLoad(t, dir, v1, "4 tickets")
	psql(t, seedLearner+"; INSERT INTO hint_opens (org_id, learner_id, item_id, n) "+
		"SELECT org_id, id, 'support-l1/service-02', 1 FROM learners WHERE github_id = 9101")

	removeTicket(t, dir)
	out, err := load(t, dir, v2)
	if err == nil || !strings.Contains(out, "items with learner progress would disappear: support-l1/service-02") {
		t.Fatalf("want a refusal naming the item, got %v\n%s", err, out)
	}
	if got := psql(t, "SELECT count(*) FROM catalog_items"); got != "4" {
		t.Fatalf("items after refusal: %s", got)
	}
}

// A first start whose insert is still uncommitted when the load runs holds a key-share lock on the
// item. The load waits for it, then sees the row and refuses: the insert is never orphaned.
func TestRemovalRefusedWhileProgressInsertIsUncommitted(t *testing.T) {
	wipe(t)
	dir := fixture(t)
	mustLoad(t, dir, v1, "4 tickets")
	psql(t, seedLearner)

	ids, err := exec.Command("docker", "ps", "-q",
		"--filter", "label=com.docker.compose.project="+os.Getenv("DOJO_COMPOSE_PROJECT"),
		"--filter", "label=com.docker.compose.service=db").Output()
	id := strings.TrimSpace(string(ids))
	if err != nil || id == "" {
		t.Fatalf("find db container: %v %q", err, ids)
	}
	holder := exec.Command("docker", "exec", "-e", "PGPASSWORD="+envExample(t, "POSTGRES_PASSWORD"), id,
		"psql", "-h", id, "-U", "dojo", "-d", "dojo", "-v", "ON_ERROR_STOP=1", "-c",
		"BEGIN; INSERT INTO ticket_progress (org_id, learner_id, item_id, state, first_started_at) "+
			"SELECT org_id, id, 'support-l1/service-02', 'started', now() FROM learners WHERE github_id = 9101; "+
			"SELECT pg_sleep(15); COMMIT")
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- holder.Wait() }()
	time.Sleep(1500 * time.Millisecond)

	removeTicket(t, dir)
	out, lerr := load(t, dir, v2)
	if lerr == nil || !strings.Contains(out, "items with learner progress would disappear: support-l1/service-02") {
		t.Fatalf("want a refusal naming the item, got %v\n%s", lerr, out)
	}
	if err := <-done; err != nil {
		t.Fatalf("the progress insert failed: %v", err)
	}
	if got := psql(t, "SELECT (SELECT count(*) FROM ticket_progress) || ',' || (SELECT count(*) FROM catalog_items)"); got != "1,4" {
		t.Fatalf("progress,items: %s", got)
	}
}

// Dropping a role in the same load that moves its tickets to a new role works: the role goes after
// the items have left it.
func TestDroppedRoleGoesAfterItsTicketsMove(t *testing.T) {
	wipe(t)
	dir := fixture(t)
	mustLoad(t, dir, v1, "4 tickets")

	rewrite := func(path string) {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(b), "support-l1", "support-l2")), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rewrite(filepath.Join(dir, "catalog.yaml"))
	if out, err := exec.Command("cp", "-R", filepath.Join(dir, "support-l1"), filepath.Join(dir, "support-l2")).CombinedOutput(); err != nil {
		t.Fatalf("copy: %v %s", err, out)
	}
	if err := os.RemoveAll(filepath.Join(dir, "support-l1")); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"perms-01", "service-02"} {
		rewrite(filepath.Join(dir, "support-l2", n, "ticket.yaml"))
	}
	mustLoad(t, dir, v2, "4 tickets")

	if got := psql(t, "SELECT string_agg(slug, ',' ORDER BY slug) FROM roles"); got != "foundations,support-l2" {
		t.Fatalf("roles: %s", got)
	}
	if got := psql(t, "SELECT count(*) || ',' || count(*) FILTER (WHERE id LIKE 'support-l2/%') FROM catalog_items"); got != "4,2" {
		t.Fatalf("items: %s", got)
	}
}
