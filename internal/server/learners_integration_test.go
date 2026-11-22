//go:build integration

package server_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kibanbuild/dojo/internal/server/repo"
)

func apiConn(t *testing.T) *pgx.Conn {
	t.Helper()
	project := os.Getenv("DOJO_COMPOSE_PROJECT")
	if project == "" {
		t.Fatal("DOJO_COMPOSE_PROJECT is not set: run `make test-integration`")
	}
	// Asked now: the published port changes when another test restarts the db.
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
	url := fmt.Sprintf("postgres://dojo_api:%s@127.0.0.1:%s/dojo?sslmode=disable", envExample(t, "DOJO_API_DB_PASSWORD"), port)
	conn, err := pgx.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func asOwner(t *testing.T, sql string) string {
	t.Helper()
	out, err := psqlAs(t, "dojo", envExample(t, "POSTGRES_PASSWORD"), sql)
	if err != nil {
		t.Fatalf("%s: %v %s", sql, err, out)
	}
	return out
}

func TestLearnerAndSessionQueries(t *testing.T) {
	resetLearners(t, "4242")
	ctx := context.Background()
	q := repo.New(apiConn(t))

	org, err := q.GetOrgBySlug(ctx, "public")
	if err != nil {
		t.Fatal(err)
	}
	now := pgtype.Timestamptz{Time: time.Now(), Valid: true}
	args := repo.UpsertLearnerParams{
		OrgID: org.ID, GithubID: 4242, GithubLogin: "Ada", Username: "ada", Language: "en",
		AgeConfirmedAt: now, TermsAcceptedAt: now, UsageStats: true,
	}
	l, err := q.UpsertLearner(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	if l.SuspendedAt.Valid || l.Email.Valid {
		t.Fatalf("new learner: %+v", l)
	}

	// A returning sign-in refreshes the login and keeps the email GitHub no longer returns.
	args.Email = pgtype.Text{String: "ada@example.com", Valid: true}
	if _, err := q.UpsertLearner(ctx, args); err != nil {
		t.Fatal(err)
	}
	args.GithubLogin, args.Email = "ada-renamed", pgtype.Text{}
	l2, err := q.UpsertLearner(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	if l2.ID != l.ID || l2.GithubLogin != "ada-renamed" || l2.Email.String != "ada@example.com" {
		t.Fatalf("upsert: %+v", l2)
	}
	if got, err := q.GetLearnerByID(ctx, l.ID); err != nil || got.ID != l.ID {
		t.Fatalf("by id: %v %+v", err, got)
	}
	if got, err := q.GetLearnerByGithubID(ctx, 4242); err != nil || got.ID != l.ID {
		t.Fatalf("by github id: %v %+v", err, got)
	}

	hash := sha256.Sum256([]byte("token"))
	s, err := q.InsertSession(ctx, repo.InsertSessionParams{OrgID: org.ID, LearnerID: l.ID, TokenHash: hash[:]})
	if err != nil {
		t.Fatal(err)
	}
	active, err := q.GetActiveSession(ctx, hash[:])
	if err != nil || active.ID != s.ID || active.LearnerID != l.ID || active.SuspendedAt.Valid {
		t.Fatalf("active: %v %+v", err, active)
	}
	if err := q.TouchSession(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	if again, err := q.GetActiveSession(ctx, hash[:]); err != nil || !again.LastUsedAt.Time.After(active.LastUsedAt.Time) {
		t.Fatalf("touch did not move last_used_at: %v %+v", err, again)
	}
	if _, err := q.GetActiveSession(ctx, make([]byte, 32)); err != pgx.ErrNoRows {
		t.Fatalf("unknown hash: %v", err)
	}

	// Suspension is read through the join; the migration role stands in for the operator.
	asOwner(t, "UPDATE learners SET suspended_at = now() WHERE github_id = 4242")
	if got, err := q.GetActiveSession(ctx, hash[:]); err != nil || !got.SuspendedAt.Valid {
		t.Fatalf("suspended: %v %+v", err, got)
	}

	// The sliding window: a session unused for 31 days is not active.
	asOwner(t, "UPDATE sessions SET last_used_at = now() - interval '31 days' WHERE learner_id = '"+l.ID.String()+"'")
	if _, err := q.GetActiveSession(ctx, hash[:]); err != pgx.ErrNoRows {
		t.Fatalf("expired: %v", err)
	}

	hash2 := sha256.Sum256([]byte("token2"))
	s2, err := q.InsertSession(ctx, repo.InsertSessionParams{OrgID: org.ID, LearnerID: l.ID, TokenHash: hash2[:]})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.RevokeSession(ctx, s2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := q.GetActiveSession(ctx, hash2[:]); err != pgx.ErrNoRows {
		t.Fatalf("revoked: %v", err)
	}

	// Removing the learner as the migration role cascades to its sessions.
	asOwner(t, "DELETE FROM learners WHERE github_id = 4242")
	if out := asOwner(t, "SELECT count(*) FROM sessions WHERE learner_id = '"+l.ID.String()+"'"); out != "0" {
		t.Fatalf("sessions after cascade: %q", out)
	}
}
