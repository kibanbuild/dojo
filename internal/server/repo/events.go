package repo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// ErrLearnerGone means the learner was deleted before the batch could be written.
var ErrLearnerGone = errors.New("learner not found")

// EventIn is one validated event of POST /v1/events (design 3.8).
type EventIn struct {
	ClientID pgtype.UUID
	Type     string
	ItemID   string
	ClientAt time.Time
	// Payload is the event's JSON without id, type, at and output: the events row never keeps output.
	Payload []byte

	Lab    string // ticket.started
	Result string // check.run: pass or fail
	Passed *int32
	Total  *int32
	Output *string
}

// EventsResult counts one batch; Accepted includes the ignored events.
type EventsResult struct{ Accepted, Duplicates, Ignored int }

// RecordEvents stores a batch and projects it into ticket_progress in one transaction, under the
// learner's progress lock. Events are handled in array order.
func (s *Store) RecordEvents(ctx context.Context, learnerID pgtype.UUID, evs []EventIn) (EventsResult, error) {
	var res EventsResult
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return res, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)

	if err := q.LockLearnerProgress(ctx, learnerID); err != nil {
		return res, fmt.Errorf("lock: %w", err)
	}
	l, err := q.GetLearnerByID(ctx, learnerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return res, ErrLearnerGone
	}
	if err != nil {
		return res, fmt.Errorf("load learner: %w", err)
	}
	nowTS, err := q.TransactionNow(ctx)
	if err != nil {
		return res, fmt.Errorf("clock: %w", err)
	}
	now := nowTS.Time

	ids := make([]string, 0, len(evs))
	for _, e := range evs {
		ids = append(ids, e.ItemID)
	}
	found, err := q.ListTicketIDs(ctx, ListTicketIDsParams{OrgID: l.OrgID, Ids: ids})
	if err != nil {
		return res, fmt.Errorf("list tickets: %w", err)
	}
	known := make(map[string]bool, len(found))
	for _, id := range found {
		known[id] = true
	}

	rows := map[string]*TicketProgress{}
	for _, e := range evs {
		_, err := q.InsertEvent(ctx, InsertEventParams{
			OrgID: l.OrgID, LearnerID: l.ID, ClientEventID: e.ClientID, Type: e.Type,
			Payload: e.Payload, ClientAt: pgtype.Timestamptz{Time: e.ClientAt, Valid: true},
		})
		if errors.Is(err, pgx.ErrNoRows) {
			res.Duplicates++
			continue
		}
		if err != nil {
			return res, fmt.Errorf("insert event: %w", err)
		}
		res.Accepted++
		if !known[e.ItemID] {
			res.Ignored++
			continue
		}

		cur, ok := rows[e.ItemID]
		if !ok {
			row, err := q.GetTicketProgress(ctx, GetTicketProgressParams{LearnerID: l.ID, ItemID: e.ItemID})
			switch {
			case err == nil:
				cur = &row
			case !errors.Is(err, pgx.ErrNoRows):
				return res, fmt.Errorf("load progress: %w", err)
			}
		}
		next := projectEvent(cur, l.OrgID, l.ID, e, now)
		if cur == nil {
			created, err := q.InsertTicketProgress(ctx, InsertTicketProgressParams{
				OrgID: next.OrgID, LearnerID: next.LearnerID, ItemID: next.ItemID, State: next.State,
				Lab: next.Lab, LabState: next.LabState, LabStateAt: next.LabStateAt,
				FirstStartedAt: next.FirstStartedAt, LastSeenAt: next.LastSeenAt, CheckCount: next.CheckCount,
				LastCheckAt: next.LastCheckAt, LastCheckOk: next.LastCheckOk, LastCheckPassed: next.LastCheckPassed,
				LastCheckTotal: next.LastCheckTotal, LastCheckOutput: next.LastCheckOutput,
				FirstPassedAt: next.FirstPassedAt, PassedSeconds: next.PassedSeconds,
			})
			if err != nil {
				return res, fmt.Errorf("insert progress: %w", err)
			}
			next = created
		} else if err := q.UpdateTicketProgress(ctx, UpdateTicketProgressParams{
			State: next.State, Lab: next.Lab, LabState: next.LabState, LabStateAt: next.LabStateAt,
			FirstStartedAt: next.FirstStartedAt, LastSeenAt: next.LastSeenAt, CheckCount: next.CheckCount,
			LastCheckAt: next.LastCheckAt, LastCheckOk: next.LastCheckOk, LastCheckPassed: next.LastCheckPassed,
			LastCheckTotal: next.LastCheckTotal, LastCheckOutput: next.LastCheckOutput,
			FirstPassedAt: next.FirstPassedAt, PassedSeconds: next.PassedSeconds,
			LearnerID: next.LearnerID, ItemID: next.ItemID,
		}); err != nil {
			return res, fmt.Errorf("update progress: %w", err)
		}
		rows[e.ItemID] = &next
	}
	if err := notifyProgress(ctx, tx, l.ID, rows); err != nil {
		return res, err
	}
	if err := tx.Commit(ctx); err != nil {
		return res, fmt.Errorf("commit: %w", err)
	}
	return res, nil
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

// projectEvent returns the progress row after one event (design 3.8, ticket_progress). cur is nil
// when the learner has no row yet; now is the server clock, which caps the client's time.
func projectEvent(cur *TicketProgress, orgID, learnerID pgtype.UUID, e EventIn, now time.Time) TicketProgress {
	at := e.ClientAt
	if at.After(now) {
		at = now
	}
	var p TicketProgress
	if cur == nil {
		p = TicketProgress{
			OrgID: orgID, LearnerID: learnerID, ItemID: e.ItemID, CreatedAt: ts(now),
			State: "started", FirstStartedAt: ts(at),
		}
	} else {
		p = *cur
	}
	p.LastSeenAt = ts(now)

	// A lab event older than the recorded lab state is stored in events and changes nothing here.
	setLab := func(state string) {
		if p.LabStateAt.Valid && at.Before(p.LabStateAt.Time) {
			return
		}
		p.LabState, p.LabStateAt = pgtype.Text{String: state, Valid: true}, ts(at)
		if e.Lab != "" {
			p.Lab = pgtype.Text{String: e.Lab, Valid: true}
		}
	}

	switch e.Type {
	case "ticket.started":
		if p.State == "passed" && at.After(p.FirstPassedAt.Time) {
			p.State = "revisit"
		}
		setLab("starting")
	case "lab.started":
		setLab("running")
	case "lab.failed":
		setLab("failed")
	case "lab.stopped":
		setLab("stopped")
	case "check.run":
		p.CheckCount++
		if !p.LastCheckAt.Valid || !at.Before(p.LastCheckAt.Time) {
			p.LastCheckAt = ts(at)
			p.LastCheckOk = pgtype.Bool{Bool: e.Result == "pass", Valid: true}
			p.LastCheckPassed, p.LastCheckTotal = pgtype.Int4{}, pgtype.Int4{}
			if e.Passed != nil && e.Total != nil {
				p.LastCheckPassed = pgtype.Int4{Int32: *e.Passed, Valid: true}
				p.LastCheckTotal = pgtype.Int4{Int32: *e.Total, Valid: true}
			}
			p.LastCheckOutput = pgtype.Text{}
			if e.Output != nil {
				p.LastCheckOutput = pgtype.Text{String: *e.Output, Valid: true}
			}
		}
		if e.Result == "pass" {
			switch p.State {
			case "started":
				p.State = "passed"
				p.FirstPassedAt = ts(now)
				secs := min(max(at.Sub(p.FirstStartedAt.Time), 0), now.Sub(p.CreatedAt.Time)).Seconds()
				p.PassedSeconds = pgtype.Int4{Int32: int32(secs), Valid: true}
			case "revisit":
				p.State = "passed"
			}
		}
	}
	return p
}
