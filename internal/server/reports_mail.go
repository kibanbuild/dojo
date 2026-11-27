package server

import (
	"context"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kibanyadev/dojo/internal/mail"
	"github.com/kibanyadev/dojo/internal/server/repo"
)

// reportBatch is how many reports one sweep forwards at most.
const reportBatch = 5

// ReportQueue claims and forwards unsent reports one at a time (repo.Store.ForwardReport).
type ReportQueue interface {
	ForwardReport(ctx context.Context, skip []pgtype.UUID, send func(context.Context, repo.Report) error) (pgtype.UUID, bool, error)
}

// ReportForwarder emails unsent reports to us (design 3.9). It is not the learner outbox: a report is an
// internal notice, claimed with a row lock on ticket_reports itself.
type ReportForwarder struct {
	Queue ReportQueue
	Mail  mail.Sender
	// To is DOJO_REPORTS_TO; Domain is the sending domain (hello@Domain).
	To     string
	Domain string
	Now    func() time.Time

	retryAt map[[16]byte]time.Time
}

func (f *ReportForwarder) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

// Sweep forwards up to reportBatch unsent reports and returns how many it sent. A report that fails is logged
// by class only (never the address or the text) and left unsent; it is skipped for the backoff of its class.
func (f *ReportForwarder) Sweep(ctx context.Context) (int, error) {
	now := f.now()
	if f.retryAt == nil {
		f.retryAt = map[[16]byte]time.Time{}
	}
	sent := 0
	for range reportBatch {
		var skip []pgtype.UUID
		for id, at := range f.retryAt {
			if !now.Before(at) {
				delete(f.retryAt, id)
				continue
			}
			skip = append(skip, pgtype.UUID{Bytes: id, Valid: true})
		}
		id, found, err := f.Queue.ForwardReport(ctx, skip, f.send)
		switch {
		case ctx.Err() != nil:
			return sent, ctx.Err()
		case !found:
			return sent, err
		case err != nil:
			f.retryAt[id.Bytes] = now.Add(logSendFailure(fmt.Sprintf("%x", id.Bytes), "report", err))
		default:
			sent++
		}
	}
	return sent, nil
}

func (f *ReportForwarder) send(ctx context.Context, r repo.Report) error {
	text := reportText(r)
	m := mail.Message{From: "hello@" + f.Domain, To: f.To, Subject: reportSubject(r), Text: text,
		HTML: "<pre>" + html.EscapeString(text) + "</pre>"}
	if mail.ValidAddress(r.Email) {
		m.ReplyTo = r.Email
	}
	return f.Mail.Send(ctx, m)
}

// RunReportForwarder sweeps once at startup, then every interval until ctx ends.
func RunReportForwarder(ctx context.Context, f *ReportForwarder, every time.Duration) {
	runSweeps(ctx, "reports", every, f.Sweep)
}

func reportSubject(r repo.Report) string {
	item := r.ItemID
	if item == "" {
		item = "-"
	}
	id := r.ID.String()
	return "[dojo report] " + r.Kind + " " + item + " " + id[len(id)-8:]
}

func reportText(r repo.Report) string {
	var b strings.Builder
	version := r.CLIVersion
	if version == "" {
		version = "-"
	}
	fmt.Fprintf(&b, "Kind: %s\nItem: %s\nLearner: %s\nCLI version: %s\n\nMessage:\n%s\n", r.Kind, orDash(r.ItemID), r.GithubLogin, version, r.Message)
	if r.DoctorSummary != "" {
		fmt.Fprintf(&b, "\nDoctor summary:\n%s\n", r.DoctorSummary)
	}
	if r.Output != "" {
		fence := "```"
		for strings.Contains(r.Output, fence) {
			fence += "`"
		}
		fmt.Fprintf(&b, "\nLast check output:\n%s\n%s\n%s\n", fence, strings.TrimRight(r.Output, "\n"), fence)
	}
	return b.String()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
