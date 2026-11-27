//go:build integration

package admin_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kibanyadev/dojo/internal/admin"
)

// adminPool connects to the stack's Postgres as dojo_admin, the role the service runs as.
func adminPool(t *testing.T) *pgxpool.Pool {
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
	addr, err := exec.Command("docker", "port", id, "5432/tcp").Output()
	if err != nil {
		t.Fatalf("db port: %v", err)
	}
	hostport := strings.Fields(string(addr))[0]
	port := hostport[strings.LastIndex(hostport, ":")+1:]
	url := fmt.Sprintf("postgres://dojo_admin:%s@127.0.0.1:%s/dojo?sslmode=disable", envExample(t, "DOJO_ADMIN_DB_PASSWORD"), port)
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// addOperatorAction is the "change" under audit: a row dojo_admin may insert.
func addOperatorAction(tx pgx.Tx) error {
	_, err := tx.Exec(context.Background(),
		`INSERT INTO operators (github_id, github_login) VALUES (1002, 'bob')`)
	return err
}

func operatorCount(t *testing.T) string {
	return psqlAsOwner(t, "SELECT count(*) FROM operators WHERE github_id = 1002")
}

func auditCount(t *testing.T, action string) string {
	return psqlAsOwner(t, "SELECT count(*) FROM admin_audit WHERE action = '"+action+"'")
}

func sessionOperator(t *testing.T, pool *pgxpool.Pool) admin.Operator {
	t.Helper()
	op, err := admin.PGStore{Pool: pool}.OperatorByGithubID(context.Background(), 1001)
	if err != nil {
		t.Fatal(err)
	}
	return op
}

func TestWithAuditCommitsActionAndAuditTogether(t *testing.T) {
	seedOperator(t, "1001", "ada", false)
	pool := adminPool(t)
	op := sessionOperator(t, pool)

	err := admin.WithAudit(context.Background(), pool, op, admin.AuditEntry{
		Action: "operator.add", TargetType: "operator", TargetID: "1002", Reason: "new on call",
		Before: nil, After: map[string]any{"github_login": admin.ChangedMarker, "role": "operator"},
	}, addOperatorAction)
	if err != nil {
		t.Fatal(err)
	}
	if operatorCount(t) != "1" {
		t.Fatal("action not committed")
	}
	row := psqlAsOwner(t, "SELECT operator_id = (SELECT id FROM operators WHERE github_id = 1001), operator_login, target_type, target_id, reason, "+
		"before IS NULL, after->>'github_login', after->>'role', at IS NOT NULL FROM admin_audit WHERE action = 'operator.add'")
	if want := "t|ada|operator|1002|new on call|t|changed|operator|t"; row != want {
		t.Fatalf("audit row = %q, want %q", row, want)
	}

	// No reason stays NULL.
	if err := admin.WithAudit(context.Background(), pool, op, admin.AuditEntry{Action: "noop", TargetType: "x", TargetID: "1"},
		func(pgx.Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if got := psqlAsOwner(t, "SELECT reason IS NULL AND before IS NULL AND after IS NULL FROM admin_audit WHERE action = 'noop'"); got != "t" {
		t.Fatalf("optional fields: %s", got)
	}
}

func TestWithAuditActionErrorRollsBackBoth(t *testing.T) {
	seedOperator(t, "1001", "ada", false)
	pool := adminPool(t)
	op := sessionOperator(t, pool)
	boom := errors.New("boom")

	err := admin.WithAudit(context.Background(), pool, op, admin.AuditEntry{Action: "operator.add", TargetType: "operator", TargetID: "1002"},
		func(tx pgx.Tx) error {
			if err := addOperatorAction(tx); err != nil {
				return err
			}
			return boom
		})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if operatorCount(t) != "0" || auditCount(t, "operator.add") != "0" {
		t.Fatalf("not rolled back: operators %s, audit %s", operatorCount(t), auditCount(t, "operator.add"))
	}
}

func TestWithAuditInsertFailureRollsBackTheAction(t *testing.T) {
	seedOperator(t, "1001", "ada", false)
	pool := adminPool(t)
	op := sessionOperator(t, pool)

	// 65 characters break the admin_audit CHECK at insert time, after the action ran.
	tooLong := strings.Repeat("a", 65)
	err := admin.WithAudit(context.Background(), pool, op, admin.AuditEntry{Action: tooLong, TargetType: "operator", TargetID: "1002"}, addOperatorAction)
	if err == nil {
		t.Fatal("audit insert should have failed")
	}
	if errors.Is(err, admin.ErrAuditEntry) {
		t.Fatalf("expected a database failure, got a validation refusal: %v", err)
	}
	if operatorCount(t) != "0" {
		t.Fatal("the action survived a failed audit insert")
	}
}

func TestWithAuditRefusesPIIAgainstTheRealDB(t *testing.T) {
	seedOperator(t, "1001", "ada", false)
	pool := adminPool(t)
	op := sessionOperator(t, pool)

	err := admin.WithAudit(context.Background(), pool, op, admin.AuditEntry{
		Action: "learner.rename", TargetType: "learner", TargetID: "1",
		Before: map[string]any{"username": "ada"}, After: map[string]any{"username": "grace"},
	}, addOperatorAction)
	if !errors.Is(err, admin.ErrAuditEntry) {
		t.Fatalf("err = %v", err)
	}
	if operatorCount(t) != "0" || auditCount(t, "learner.rename") != "0" {
		t.Fatal("something was written for a refused entry")
	}
}

// The audit log is append-only for the service role.
func TestAuditRowsCannotBeChangedByTheServiceRole(t *testing.T) {
	seedOperator(t, "1001", "ada", false)
	pool := adminPool(t)
	op := sessionOperator(t, pool)
	if err := admin.WithAudit(context.Background(), pool, op, admin.AuditEntry{Action: "noop", TargetType: "x", TargetID: "1"},
		func(pgx.Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{"UPDATE admin_audit SET reason = 'x'", "DELETE FROM admin_audit"} {
		if _, err := pool.Exec(context.Background(), sql); err == nil {
			t.Errorf("%s was allowed", sql)
		}
	}
}

// A panic in the action must not leak the transaction: nothing is written and the connection returns.
func TestWithAuditPanicRollsBackAndReleasesTheConnection(t *testing.T) {
	seedOperator(t, "1001", "ada", false)
	pool := adminPool(t)
	op := sessionOperator(t, pool)

	func() {
		defer func() {
			if recover() == nil {
				t.Error("the panic was swallowed")
			}
		}()
		_ = admin.WithAudit(context.Background(), pool, op, admin.AuditEntry{Action: "operator.add", TargetType: "operator", TargetID: "1002"},
			func(tx pgx.Tx) error {
				if err := addOperatorAction(tx); err != nil {
					return err
				}
				panic("boom")
			})
	}()
	if operatorCount(t) != "0" || auditCount(t, "operator.add") != "0" {
		t.Fatalf("written after a panic: operators %s, audit %s", operatorCount(t), auditCount(t, "operator.add"))
	}
	if n := pool.Stat().AcquiredConns(); n != 0 {
		t.Fatalf("%d connections still held", n)
	}
}
