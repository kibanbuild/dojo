package repo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Report limits (design 3.9): per learner, counted from ticket_reports.created_at.
const (
	reportsPerHour = 5
	reportsPerDay  = 20
)

// ErrUnknownItem is a report for an item that is not a ticket in the catalog.
var ErrUnknownItem = errors.New("unknown item")

// ReportIn is a validated report. ItemID is empty for setup help with no ticket.
type ReportIn struct {
	LearnerID     pgtype.UUID
	Kind          string
	ItemID        string
	Message       string
	IncludeOutput bool
}

// ReportResult is what CreateReport did: the new id, or how long until a slot frees.
type ReportResult struct {
	ID         pgtype.UUID
	RetryAfter time.Duration // positive when the learner is over the limit
}

// CreateReport stores a report, unless the learner is over the limit. The learner row stays locked from the
// count to the insert, so concurrent requests cannot both pass.
func (s *Store) CreateReport(ctx context.Context, in ReportIn) (ReportResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ReportResult{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)

	item := pgtype.Text{String: in.ItemID, Valid: in.ItemID != ""}
	if item.Valid {
		found, err := q.ReportItemExists(ctx, in.ItemID)
		if err != nil {
			return ReportResult{}, fmt.Errorf("check item: %w", err)
		}
		if !found {
			return ReportResult{}, ErrUnknownItem
		}
	}
	if _, err := q.LockLearnerForReport(ctx, in.LearnerID); err != nil {
		return ReportResult{}, fmt.Errorf("lock learner: %w", err)
	}
	c, err := q.CountRecentReports(ctx, in.LearnerID)
	if err != nil {
		return ReportResult{}, fmt.Errorf("count: %w", err)
	}
	if wait := reportWait(c); wait > 0 {
		return ReportResult{RetryAfter: wait}, nil
	}
	id, err := q.InsertReport(ctx, InsertReportParams{
		ItemID: item, Kind: in.Kind, IncludeOutput: in.IncludeOutput, Message: in.Message, LearnerID: in.LearnerID,
	})
	if err != nil {
		return ReportResult{}, fmt.Errorf("insert: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ReportResult{}, fmt.Errorf("commit: %w", err)
	}
	return ReportResult{ID: id}, nil
}

// reportWait is the time until the oldest counted report leaves the exceeded window; zero when under both limits.
func reportWait(c CountRecentReportsRow) time.Duration {
	var wait time.Duration
	if c.HourCount >= reportsPerHour {
		wait = max(wait, c.HourOldest.Time.Add(time.Hour).Sub(c.DbNow.Time))
	}
	if c.DayCount >= reportsPerDay {
		wait = max(wait, c.DayOldest.Time.Add(24*time.Hour).Sub(c.DbNow.Time))
	}
	if wait <= 0 && (c.HourCount >= reportsPerHour || c.DayCount >= reportsPerDay) {
		return time.Second
	}
	return wait
}

// Report is an unsent report held under the reader's row lock, with the learner facts the mail needs.
type Report struct {
	ID            pgtype.UUID
	Kind          string
	ItemID        string
	CLIVersion    string
	Output        string
	Message       string
	DoctorSummary string
	GithubLogin   string
	Email         string
}

// ForwardReport claims the oldest unsent report not in skip, runs send and sets emailed_at, all in one
// transaction, so two tasks never send one report and a failed send leaves the row for the next pass. found is
// false when nothing is left to claim; when send fails the claimed id is returned with the error, so the
// caller can back that row off. A crash between send and commit sends again: at least once.
func (s *Store) ForwardReport(ctx context.Context, skip []pgtype.UUID, send func(context.Context, Report) error) (id pgtype.UUID, found bool, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return id, false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)

	row, err := q.ClaimReport(ctx, skip)
	if errors.Is(err, pgx.ErrNoRows) {
		return id, false, nil
	}
	if err != nil {
		return id, false, fmt.Errorf("claim: %w", err)
	}
	r := Report{ID: row.ID, Kind: row.Kind, ItemID: row.ItemID.String, CLIVersion: row.CliVersion,
		Output: row.LastCheckOutput.String, Message: row.Message, DoctorSummary: row.DoctorSummary,
		GithubLogin: row.GithubLogin, Email: row.Email.String}
	if err := send(ctx, r); err != nil {
		return row.ID, true, err
	}
	if err := q.MarkReportEmailed(ctx, row.ID); err != nil {
		return row.ID, true, fmt.Errorf("mark emailed: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return row.ID, true, fmt.Errorf("commit: %w", err)
	}
	return row.ID, true, nil
}
