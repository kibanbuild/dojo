package repo

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// CreateLearner inserts a new learner and queues their welcome mail (E1) in the same statement. When the
// GitHub account already has a learner (a concurrent first sign-in), it refreshes that one instead and
// queues nothing: the winner queued the mail.
func (q *Queries) CreateLearner(ctx context.Context, a UpsertLearnerParams) (Learner, error) {
	row, err := q.InsertLearnerWithWelcome(ctx, InsertLearnerWithWelcomeParams(a))
	if errors.Is(err, pgx.ErrNoRows) {
		return q.UpsertLearner(ctx, a)
	}
	if err != nil {
		return Learner{}, err
	}
	return Learner(row), nil
}

// EmailClaimLockClass is the advisory-lock class of the outbox row claim (design 3.8 lock list): the repo keeps
// small per-purpose constants, 1 and 2 in events, 3 in catalogload, 5 here.
const EmailClaimLockClass int32 = 5

// Email is an unsent outbox row held under the sender's lock.
type Email struct {
	ID        pgtype.UUID
	LearnerID pgtype.UUID
	Kind      string
	To        string
}

// Delivery is what DeliverEmail did with a row.
type Delivery int

const (
	// Skipped: another task holds the row, or it is no longer unsent. Nothing was sent.
	Skipped Delivery = iota
	// Sent: the mail went out and the row is marked.
	Sent
	// SentUnmarked: the mail went out but the row was gone before the mark (the learner was deleted mid-send).
	SentUnmarked
)

// DeliverEmail runs send for one outbox row and marks it sent, all in one transaction that holds the row's
// advisory lock, so two API tasks never send the same mail. It returns Skipped, with no error, when another
// task holds the row or it is no longer unsent. When send fails nothing is marked and the row stays
// queued. The mail is sent before the mark, so a crash between the two sends it again: at least once.
func (s *Store) DeliverEmail(ctx context.Context, id pgtype.UUID, send func(context.Context, Email) error) (Delivery, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Skipped, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)

	locked, err := q.EmailTryLock(ctx, EmailTryLockParams{Class: EmailClaimLockClass, ID: id})
	if err != nil {
		return Skipped, fmt.Errorf("lock: %w", err)
	}
	if !locked {
		return Skipped, nil
	}
	row, err := q.GetEmailForSend(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Skipped, nil
	}
	if err != nil {
		return Skipped, fmt.Errorf("read row: %w", err)
	}
	if err := send(ctx, Email{ID: row.ID, LearnerID: row.LearnerID, Kind: row.Kind, To: row.ToAddress.String}); err != nil {
		return Skipped, err
	}
	marked, err := q.MarkEmailSent(ctx, id)
	if err != nil {
		return Skipped, fmt.Errorf("mark sent: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Skipped, fmt.Errorf("commit: %w", err)
	}
	if !marked {
		return SentUnmarked, nil
	}
	return Sent, nil
}

// ListUnsentEmails reads up to max unsent rows of the given kinds, not in skip, that still have an address, oldest first.
func (s *Store) ListUnsentEmails(ctx context.Context, kinds []string, skip []pgtype.UUID, max int32) ([]ListUnsentEmailsRow, error) {
	return s.q.ListUnsentEmails(ctx, ListUnsentEmailsParams{Kinds: kinds, Skip: skip, Max: max})
}
