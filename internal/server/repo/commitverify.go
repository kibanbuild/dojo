package repo

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
)

// CommitLease is one pending commit claimed by a verifier task (design 3.9). Expired is true when
// 7 days have passed since the sha was accepted.
type CommitLease struct {
	LearnerID   pgtype.UUID
	ItemID      string
	Commit      string
	Tries       int32
	Expired     bool
	GithubID    int64
	GithubLogin string
}

// leaseCommitsSQL is the lease of design 3.9, wrapped to also return the learner's GitHub identity.
const leaseCommitsSQL = `
WITH leased AS (
    UPDATE ticket_progress SET commit_next_at = now() + interval '5 minutes'
    WHERE (learner_id, item_id) IN (
        SELECT learner_id, item_id FROM ticket_progress
        WHERE commit_state = 'pending' AND commit_next_at <= now()
        ORDER BY commit_next_at LIMIT $1 FOR UPDATE SKIP LOCKED)
    RETURNING learner_id, item_id, passed_commit, commit_tries, commit_at)
SELECT s.learner_id, s.item_id, s.passed_commit, s.commit_tries, s.commit_at + interval '7 days' <= now(), l.github_id, l.github_login
FROM leased s JOIN learners l ON l.id = s.learner_id`

// LeaseCommits claims up to n due pending commits for 5 minutes. Rows another task holds are skipped.
func (s *Store) LeaseCommits(ctx context.Context, n int) ([]CommitLease, error) {
	rows, err := s.pool.Query(ctx, leaseCommitsSQL, n)
	if err != nil {
		return nil, fmt.Errorf("lease commits: %w", err)
	}
	defer rows.Close()
	var out []CommitLease
	for rows.Next() {
		var l CommitLease
		if err := rows.Scan(&l.LearnerID, &l.ItemID, &l.Commit, &l.Tries, &l.Expired, &l.GithubID, &l.GithubLogin); err != nil {
			return nil, fmt.Errorf("lease commits: %w", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// CommitResult is the outcome of one check. State is pending (a try counted, retry after DelaySeconds),
// verified or unverified.
type CommitResult struct {
	Lease        CommitLease
	State        string
	Tries        int32
	DelaySeconds float64
}

const finishCommitSQL = `
UPDATE ticket_progress SET updated_at = now(), commit_state = $3::text, commit_tries = $4,
    commit_next_at = CASE WHEN $3::text = 'pending' THEN now() + make_interval(secs => $5::float8) END,
    commit_checked_at = CASE WHEN $3::text = 'verified' THEN now() ELSE commit_checked_at END
WHERE learner_id = $1 AND item_id = $2 AND commit_state = 'pending' AND passed_commit = $6`

// FinishCommit writes a check's result to the commit columns only, and only while the row still holds the
// sha that was read, so a recommit that landed meanwhile is never overwritten. A change of state is
// announced to the learner's streams. It reports whether the row was updated.
func (s *Store) FinishCommit(ctx context.Context, r CommitResult) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	l := r.Lease
	tag, err := tx.Exec(ctx, finishCommitSQL, l.LearnerID, l.ItemID, r.State, r.Tries, r.DelaySeconds, l.Commit)
	if err != nil {
		return false, fmt.Errorf("finish commit: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	if r.State != "pending" {
		row, err := s.q.WithTx(tx).GetTicketProgress(ctx, GetTicketProgressParams{LearnerID: l.LearnerID, ItemID: l.ItemID})
		if err != nil {
			return false, fmt.Errorf("read progress: %w", err)
		}
		if err := notifyProgress(ctx, tx, l.LearnerID, map[string]*TicketProgress{l.ItemID: &row}); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit: %w", err)
	}
	return true, nil
}

// SetGithubLogin stores the login GitHub now reports for the learner's account id.
func (s *Store) SetGithubLogin(ctx context.Context, learner pgtype.UUID, login string) error {
	if _, err := s.pool.Exec(ctx, "UPDATE learners SET github_login = $2, updated_at = now() WHERE id = $1", learner, login); err != nil {
		return fmt.Errorf("set github login: %w", err)
	}
	return nil
}
