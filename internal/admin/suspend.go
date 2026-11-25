package admin

import (
	"context"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

var (
	// ErrAlreadyInState means the learner is already suspended (or already active): nothing was written.
	ErrAlreadyInState = errors.New("learner already in that state")
	errBadReason      = errors.New("a reason of 1 to 1000 characters is required")
)

// LearnerStatus is what a confirm page shows.
type LearnerStatus struct {
	ID          string
	Username    string
	SuspendedAt *time.Time
}

// Status implements LearnerStore.
func (s PGLearners) Status(ctx context.Context, id string) (LearnerStatus, error) {
	if !uuidShape.MatchString(id) {
		return LearnerStatus{}, ErrNotFound
	}
	var st LearnerStatus
	err := s.Pool.QueryRow(ctx, `SELECT id::text, username, suspended_at FROM learners WHERE id = $1::uuid`, id).
		Scan(&st.ID, &st.Username, &st.SuspendedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return LearnerStatus{}, ErrNotFound
	}
	return st, err
}

// SetSuspended implements LearnerStore. The learner is read first so the audit entry can name the
// before value; the UPDATE then matches that value, so a concurrent change refuses instead of
// recording a wrong before. The design sends no email for either action (no E-kind covers it).
func (s PGLearners) SetSuspended(ctx context.Context, op Operator, id string, suspend bool, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" || utf8.RuneCountInString(reason) > maxReasonLen {
		return errBadReason
	}
	cur, err := s.Status(ctx, id)
	if err != nil {
		return err
	}
	if suspend == (cur.SuspendedAt != nil) {
		return ErrAlreadyInState
	}
	entry := AuditEntry{TargetType: "learner", TargetID: cur.ID, Reason: reason}
	var query string
	var arg any
	if suspend {
		now := time.Now().UTC().Truncate(time.Microsecond)
		entry.Action = SuspendAction
		entry.Before = map[string]any{"suspended_at": nil}
		entry.After = map[string]any{"suspended_at": now.Format(time.RFC3339Nano)}
		query = `UPDATE learners SET suspended_at = $2, updated_at = $2 WHERE id = $1::uuid AND suspended_at IS NULL`
		arg = now
	} else {
		entry.Action = UnsuspendAction
		entry.Before = map[string]any{"suspended_at": cur.SuspendedAt.UTC().Format(time.RFC3339Nano)}
		entry.After = map[string]any{"suspended_at": nil}
		query = `UPDATE learners SET suspended_at = NULL, updated_at = now() WHERE id = $1::uuid AND suspended_at = $2`
		arg = *cur.SuspendedAt
	}
	return WithAudit(ctx, s.Pool, op, entry, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, query, cur.ID, arg)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrAlreadyInState
		}
		return nil
	})
}

var confirmPage = template.Must(template.New("confirm").Parse(layoutTemplates + `{{template "head" .}}
<p class="back"><a href="/learners/{{.ID}}">Back to @{{.Username}}</a></p>
<h1>{{.Verb}} @{{.Username}}</h1>
{{if .Error}}<p class="alert" role="alert">{{.Error}}</p>{{end}}
{{if .Form}}<section class="card" aria-labelledby="change"><h2 id="change">What will change</h2>
<ul class="effects">{{range .Effects}}<li>{{.}}</li>
{{end}}</ul></section>
<form class="card stack" method="post" action="/learners/{{.ID}}/{{.Path}}">
<input type="hidden" name="csrf_token" value="{{.CSRF}}">
<div class="field"><label for="reason">Reason (required, kept in the audit log)</label>
<textarea id="reason" name="reason" rows="4" cols="60" maxlength="1000" required>{{.Reason}}</textarea>
<span class="hint">Do not put personal data in the reason.</span></div>
<div class="row"><a class="btn" href="/learners/{{.ID}}">Cancel</a>
<button class="btn {{if .Danger}}btn-danger{{else}}btn-primary{{end}}" type="submit">{{.Verb}} account</button></div>
</form>{{end}}
{{template "foot" .}}`))

type confirmData struct {
	Title, CSRF, ID, Username, Verb, Path, Error, Reason string
	Effects                                              []string
	Form, Danger                                         bool
}

var (
	suspendEffects = []string{
		"Every API call from their web sessions and CLI tokens gets account_suspended: the web app shows that the account is suspended and the CLI stops working. Nothing is revoked.",
		"They can still sign out and delete their own account.",
		"Their progress and data are kept. No email is sent.",
	}
	unsuspendEffects = []string{
		"Their existing web sessions and CLI tokens work again.",
		"Nothing else changes. No email is sent.",
	}
)

func (l *lookup) confirmData(r *http.Request, st LearnerStatus, suspend bool) confirmData {
	d := confirmData{Title: "Suspend", CSRF: csrfFrom(r.Context()), ID: st.ID, Username: st.Username,
		Verb: "Suspend", Path: "suspend", Effects: suspendEffects, Form: true, Danger: true}
	if !suspend {
		d.Title, d.Verb, d.Path, d.Effects, d.Danger = "Unsuspend", "Unsuspend", "unsuspend", unsuspendEffects, false
	}
	if suspend == (st.SuspendedAt != nil) {
		d.Form = false
		d.Error = "This account is already " + map[bool]string{true: "suspended", false: "active"}[suspend] + ". Nothing was changed."
	}
	return d
}

// confirm shows what the action will change and asks for the reason. It reads no personal data and
// writes nothing.
func (l *lookup) confirm(suspend bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), lookupTimeout)
		defer cancel()
		st, err := l.Learners.Status(ctx, r.PathValue("id"))
		if !l.statusOK(w, r, err) {
			return
		}
		d := l.confirmData(r, st, suspend)
		code := http.StatusOK
		if !d.Form {
			code = http.StatusConflict
		}
		render(w, code, confirmPage, d)
	}
}

// act runs the change, then redirects to the learner page (post-redirect-get).
func (l *lookup) act(suspend bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), lookupTimeout)
		defer cancel()
		st, err := l.Learners.Status(ctx, r.PathValue("id"))
		if !l.statusOK(w, r, err) {
			return
		}
		d := l.confirmData(r, st, suspend)
		if !d.Form {
			render(w, http.StatusConflict, confirmPage, d)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		reason := strings.TrimSpace(r.PostForm.Get("reason"))
		d.Reason = reason
		if reason == "" {
			err = errBadReason
		} else {
			err = l.Learners.SetSuspended(ctx, operatorFrom(r.Context()), st.ID, suspend, reason)
		}
		switch {
		case errors.Is(err, errBadReason):
			d.Error = "Write a reason of 1 to 1,000 characters. Nothing was changed."
			render(w, http.StatusBadRequest, confirmPage, d)
		case errors.Is(err, ErrAlreadyInState):
			d.Form, d.Error = false, "This account was changed by someone else just now. Nothing was changed."
			render(w, http.StatusConflict, confirmPage, d)
		case errors.Is(err, ErrNotFound):
			http.NotFound(w, r)
		case err != nil:
			slog.Error("learner suspend change failed", "route", r.Pattern, "error", oneLine(err))
			http.Error(w, "change failed", http.StatusInternalServerError)
		default:
			http.Redirect(w, r, "/learners/"+st.ID, http.StatusSeeOther)
		}
	}
}

func (l *lookup) statusOK(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, ErrNotFound):
		http.NotFound(w, r)
	default:
		slog.Error("learner status failed", "route", r.Pattern, "error", oneLine(err))
		http.Error(w, "lookup failed", http.StatusInternalServerError)
	}
	return false
}
