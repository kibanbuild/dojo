package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/textproto"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kibanbuild/dojo/internal/mail"
	"github.com/kibanbuild/dojo/internal/server/repo"
)

const (
	// OutboxInterval is how often the API scans the email outbox (design 3.3).
	OutboxInterval = 5 * time.Second
	outboxBatch    = 20
	// A row that failed to send is left out of the next scans, so it cannot fill the batch and starve the rows
	// behind it, in this task's memory only: a transient failure (network, SMTP 4xx) for transientBackoff, a
	// permanent one (SMTP 5xx) for permanentBackoff.
	transientBackoff = 5 * time.Minute
	permanentBackoff = time.Hour

	// welcomeFrom is the local part E1 is sent from; hello@ is for the mails with no persona.
	welcomeFrom = "carmen"
)

// OutboxQueue is the outbox the sender works through.
type OutboxQueue interface {
	ListUnsentEmails(ctx context.Context, kinds []string, skip []pgtype.UUID, max int32) ([]repo.ListUnsentEmailsRow, error)
	DeliverEmail(ctx context.Context, id pgtype.UUID, send func(context.Context, repo.Email) error) (repo.Delivery, error)
}

// OutboxReader is what a template reads besides the row.
type OutboxReader interface {
	GetLearnerByID(ctx context.Context, id pgtype.UUID) (repo.Learner, error)
	ListCatalogPaths(ctx context.Context) ([]repo.ListCatalogPathsRow, error)
}

// Outbox delivers queued email over a mail.Sender. Only kinds with a template are sent; the rest wait
// in the table until their template is built (design 3.3).
type Outbox struct {
	Queue  OutboxQueue
	Reader OutboxReader
	Mail   mail.Sender
	// Domain is the sending domain (carmen@Domain); Origin is PUBLIC_ORIGIN without a trailing slash.
	Domain string
	Origin string
	Now    func() time.Time

	retryAt map[[16]byte]time.Time // row id -> when it may be tried again
}

func (o *Outbox) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// templated lists the kinds that have a template.
var templated = []string{"E1"}

// Sweep sends the unsent rows once and returns how many it sent. A row that fails is logged and left
// queued; the sweep goes on with the next one.
func (o *Outbox) Sweep(ctx context.Context) (int, error) {
	now := o.now()
	if o.retryAt == nil {
		o.retryAt = map[[16]byte]time.Time{}
	}
	var skip []pgtype.UUID
	for id, at := range o.retryAt {
		if !now.Before(at) {
			delete(o.retryAt, id)
			continue
		}
		skip = append(skip, pgtype.UUID{Bytes: id, Valid: true})
	}
	rows, err := o.Queue.ListUnsentEmails(ctx, templated, skip, outboxBatch)
	if err != nil {
		return 0, fmt.Errorf("list unsent: %w", err)
	}
	sent := 0
	for _, r := range rows {
		res, err := o.Queue.DeliverEmail(ctx, r.ID, o.send)
		id := fmt.Sprintf("%x", r.ID.Bytes)
		switch {
		case ctx.Err() != nil:
			return sent, ctx.Err()
		case err != nil:
			o.noteFailure(r, id, err, now)
		case res == repo.SentUnmarked:
			sent++
			slog.Info("email sent but its row was gone before the mark", "id", id, "kind", r.Kind)
		case res == repo.Sent:
			sent++
		}
	}
	return sent, nil
}

// errTemplate marks a failure while preparing the message (a read or the render), not while sending it.
var errTemplate = errors.New("template")

// noteFailure backs a failed row off and logs what kind of failure it was. An error from the mail adapter
// or the server can carry the recipient (an SMTP reply echoes it), so only the class, the SMTP code and
// the row id are logged, and the error text only for a network failure, which carries no address.
// An SMTP 5xx and an invalid address are permanent: retried in an hour, not minutes.
func (o *Outbox) noteFailure(r repo.ListUnsentEmailsRow, id string, err error, now time.Time) {
	var se *textproto.Error
	var ne net.Error
	attrs := []any{"id", id, "kind", r.Kind}
	wait, level, class := transientBackoff, slog.LevelError, "other"
	switch {
	case errors.As(err, &se):
		class = "smtp"
		attrs = append(attrs, "smtp_code", se.Code)
		if se.Code >= 500 {
			wait, level = permanentBackoff, slog.LevelWarn
		}
	case errors.Is(err, mail.ErrBadAddress), errors.Is(err, mail.ErrBadSubject):
		class, wait, level = "invalid_address", permanentBackoff, slog.LevelWarn
	case errors.Is(err, errTemplate):
		class = "template"
	case errors.As(err, &ne), errors.Is(err, context.DeadlineExceeded):
		class = "network"
		attrs = append(attrs, "error", err.Error())
	}
	o.retryAt[r.ID.Bytes] = now.Add(wait)
	slog.Log(context.Background(), level, "email send failed", append(attrs, "class", class, "retry_in", wait.String())...)
}

// send renders and sends one row. It runs inside DeliverEmail's transaction, under the row's lock.
func (o *Outbox) send(ctx context.Context, e repo.Email) error {
	if e.Kind != "E1" {
		return fmt.Errorf("no template for %s", e.Kind)
	}
	if !e.LearnerID.Valid {
		return fmt.Errorf("%w: welcome mail has no learner", errTemplate)
	}
	l, err := o.Reader.GetLearnerByID(ctx, e.LearnerID)
	if err != nil {
		return fmt.Errorf("%w: load learner: %w", errTemplate, err)
	}
	// Until O0 lands, the learner's path is the first available one, as GET /v1/path reads it.
	paths, err := o.Reader.ListCatalogPaths(ctx)
	if err != nil {
		return fmt.Errorf("%w: load paths: %w", errTemplate, err)
	}
	d := welcomeData{Username: l.Username, Link: o.Origin + "/app"}
	for _, p := range paths {
		if p.Status == "available" {
			d.PathTitle = p.Title
			break
		}
	}
	html, err := renderWelcomeHTML(d)
	if err != nil {
		return fmt.Errorf("%w: render: %w", errTemplate, err)
	}
	return o.Mail.Send(ctx, mail.Message{From: welcomeFrom + "@" + o.Domain, To: e.To,
		Subject: welcomeSubject, Text: welcomeText(d), HTML: html})
}

// RunOutbox sweeps once at startup, then every interval until ctx ends. A failed sweep is logged and
// retried next tick.
func RunOutbox(ctx context.Context, o *Outbox, every time.Duration) {
	o.Origin = strings.TrimRight(o.Origin, "/")
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		n, err := o.Sweep(ctx)
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			slog.Error("outbox sweep failed", "error", err)
		case n > 0:
			slog.Info("sent queued emails", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
