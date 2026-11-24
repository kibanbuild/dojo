package repo

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var (
	// ErrTicketNotFound means no such ticket, or a hint number outside the ticket's hints.
	ErrTicketNotFound = errors.New("ticket not found")
	// ErrHintLocked means hint n - 1 is not open yet (design 3.8).
	ErrHintLocked = errors.New("hint locked")
)

// OpenHint opens hint n of a ticket for the learner and returns its text. A first open writes the
// hint_opens row and the hint.opened event in one transaction; a repeat writes nothing. It never
// touches ticket_progress. Hint n above 1 needs hint n - 1 open, unless the ticket is passed.
func (s *Store) OpenHint(ctx context.Context, learnerID pgtype.UUID, ticket string, n int16) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)

	l, err := q.GetLearnerByID(ctx, learnerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrLearnerGone
	}
	if err != nil {
		return "", fmt.Errorf("load learner: %w", err)
	}
	t, err := q.GetTicketView(ctx, ticket)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrTicketNotFound
	}
	if err != nil {
		return "", fmt.Errorf("load ticket: %w", err)
	}
	if n < 1 || int(n) > len(t.Hints) {
		return "", ErrTicketNotFound
	}
	if n > 1 {
		prev, err := q.HintIsOpen(ctx, HintIsOpenParams{LearnerID: l.ID, ItemID: ticket, N: n - 1})
		if err != nil {
			return "", fmt.Errorf("check previous hint: %w", err)
		}
		if !prev {
			passed, err := q.TicketIsPassed(ctx, TicketIsPassedParams{LearnerID: l.ID, ItemID: ticket})
			if err != nil {
				return "", fmt.Errorf("check passed: %w", err)
			}
			if !passed {
				return "", ErrHintLocked
			}
		}
	}
	_, err = q.InsertHintOpen(ctx, InsertHintOpenParams{OrgID: l.OrgID, LearnerID: l.ID, ItemID: ticket, N: n})
	switch {
	case errors.Is(err, pgx.ErrNoRows): // already open
	case err != nil:
		return "", fmt.Errorf("insert hint open: %w", err)
	default:
		if err := q.InsertHintOpenedEvent(ctx, InsertHintOpenedEventParams{OrgID: l.OrgID, LearnerID: l.ID, ItemID: ticket, N: n}); err != nil {
			return "", fmt.Errorf("insert event: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}
	return t.Hints[n-1], nil
}
