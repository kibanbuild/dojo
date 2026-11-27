//go:build integration

package server_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Deleting a learner locks the learner row, then its outbox rows (delete_learner, or the SET NULL foreign
// key of a plain DELETE). mark_email_sent() must take the learner lock first too: locking the outbox row
// and then reaching for the learner (the email_log foreign key) closes a cycle with the delete.
// The delete holds the learner lock; mark_email_sent starts and blocks; the delete then needs the outbox row.
func TestMarkEmailSentLockOrderAgainstLearnerDelete(t *testing.T) {
	const gh, marker = "1006", "it-lock-order"
	clean := func() {
		asOwner(t, "DELETE FROM email_outbox WHERE ref LIKE '"+marker+"%'")
		asOwner(t, "DELETE FROM learners WHERE github_id = "+gh)
	}
	clean()
	t.Cleanup(clean)
	learner := strings.Fields(asOwner(t, "INSERT INTO learners (org_id, github_id, github_login, username, email, age_confirmed_at, terms_accepted_at, terms_version) "+
		"SELECT id, "+gh+", 'lo', 'lock-order', 'lo@example.test', now(), now(), 'x' FROM orgs WHERE slug = 'public' RETURNING id"))[0]
	// E2 has no template, so the API's sender leaves the row alone.
	row := strings.Fields(asOwner(t, "INSERT INTO email_outbox (learner_id, to_address, kind, ref) VALUES ('"+learner+"', 'lo@example.test', 'E2', '"+marker+"') RETURNING id"))[0]

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	connect := func(user, password, app string) *pgx.Conn {
		cfg, err := pgx.ParseConfig(apiDBURL(t))
		if err != nil {
			t.Fatal(err)
		}
		cfg.User, cfg.Password = user, password
		cfg.RuntimeParams["application_name"] = app
		conn, err := pgx.ConnectConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close(context.Background()) })
		return conn
	}
	deleter := connect("dojo", envExample(t, "POSTGRES_PASSWORD"), "lockorder-delete")
	marker1 := connect("dojo_api", envExample(t, "DOJO_API_DB_PASSWORD"), "lockorder-mark")
	probe := connect("dojo", envExample(t, "POSTGRES_PASSWORD"), "lockorder-probe")

	tx, err := deleter.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, "SELECT 1 FROM learners WHERE id = $1 FOR UPDATE", learner); err != nil {
		t.Fatal(err)
	}

	markDone := make(chan error, 1)
	go func() {
		var marked bool
		markDone <- marker1.QueryRow(ctx, "SELECT mark_email_sent($1::uuid)", row).Scan(&marked)
	}()

	// Wait until mark_email_sent is parked on a lock, so the delete's next step is the contended one.
	for blocked := false; !blocked; {
		if ctx.Err() != nil {
			t.Fatal("mark_email_sent never blocked behind the learner lock")
		}
		if err := probe.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE application_name = 'lockorder-mark' AND wait_event_type = 'Lock')").Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	_, delErr := tx.Exec(ctx, "DELETE FROM learners WHERE id = $1", learner)
	if delErr == nil {
		delErr = tx.Commit(ctx)
	}
	markErr := <-markDone

	for name, err := range map[string]error{"delete": delErr, "mark_email_sent": markErr} {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "40P01" {
			t.Errorf("%s: deadlock: %v", name, err)
		} else if err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
