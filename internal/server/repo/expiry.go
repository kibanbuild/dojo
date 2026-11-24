package repo

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
)

// ExpireLabs is one expiry sweep (design 3.8): live labs unseen for 7 days become expired and each
// learner is notified. It returns how many rows it expired. Each batch of 100 is its own
// transaction, so a returning learner never waits on a long sweep; the sweep stops when another
// task holds the sweep lock.
func (s *Store) ExpireLabs(ctx context.Context) (int, error) {
	total := 0
	after := ListStaleLabsParams{AfterLearner: pgtype.UUID{Valid: true}}
	for {
		n, next, more, err := s.expireBatch(ctx, after)
		total += n
		if err != nil || !more {
			return total, err
		}
		after = next
	}
}

// expireBatch expires one batch after the given key; more is false when the sweep is over.
func (s *Store) expireBatch(ctx context.Context, after ListStaleLabsParams) (expired int, next ListStaleLabsParams, more bool, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, after, false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)

	locked, err := q.TryLockExpirySweep(ctx)
	if err != nil {
		return 0, after, false, fmt.Errorf("sweep lock: %w", err)
	}
	if !locked {
		return 0, after, false, nil
	}
	batch, err := q.ListStaleLabs(ctx, after)
	if err != nil {
		return 0, after, false, fmt.Errorf("list stale labs: %w", err)
	}
	if len(batch) == 0 {
		return 0, after, false, nil
	}
	last := batch[len(batch)-1]
	next = ListStaleLabsParams{AfterLearner: last.LearnerID, AfterItem: last.ItemID}

	// Rows come ordered by learner, so each learner's items are consecutive.
	for i := 0; i < len(batch); {
		j, items := i, []string{}
		for ; j < len(batch) && batch[j].LearnerID == batch[i].LearnerID; j++ {
			items = append(items, batch[j].ItemID)
		}
		learner := batch[i].LearnerID
		i = j

		// The lock comes before the update, and the update repeats the staleness test:
		// a projection may have refreshed the row since the list.
		if err := q.LockLearnerProgress(ctx, learner); err != nil {
			return 0, after, false, fmt.Errorf("learner lock: %w", err)
		}
		done, err := q.ExpireLearnerLabs(ctx, ExpireLearnerLabsParams{LearnerID: learner, ItemIds: items})
		if err != nil {
			return 0, after, false, fmt.Errorf("expire labs: %w", err)
		}
		if len(done) == 0 {
			continue
		}
		rows := make(map[string]*TicketProgress, len(done))
		for k := range done {
			rows[done[k].ItemID] = &done[k]
		}
		if err := notifyProgress(ctx, tx, learner, rows); err != nil {
			return 0, after, false, err
		}
		expired += len(done)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, after, false, fmt.Errorf("commit: %w", err)
	}
	return expired, next, true, nil
}
