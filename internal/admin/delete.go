package admin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

var errBadTyped = errors.New("the typed username does not match")

// Delete implements LearnerStore. delete_learner runs inside the audit transaction, so a failed
// audit insert brings the learner back, and a false answer (unknown id, or already deleted) writes
// nothing. The audit row names the learner by id only; the E8 email row is queued by the function.
func (s PGLearners) Delete(ctx context.Context, op Operator, id, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" || utf8.RuneCountInString(reason) > maxReasonLen {
		return errBadReason
	}
	if !uuidShape.MatchString(id) {
		return ErrNotFound
	}
	return WithAudit(ctx, s.Pool, op, AuditEntry{
		Action: DeleteAction, TargetType: "learner", TargetID: id, Reason: reason,
		Before: map[string]any{"deleted": false}, After: map[string]any{"deleted": true},
	}, func(tx pgx.Tx) error {
		var deleted bool
		if err := tx.QueryRow(ctx, `SELECT delete_learner($1::uuid)`, id).Scan(&deleted); err != nil {
			return err
		}
		if !deleted {
			return ErrNotFound
		}
		return nil
	})
}

var deleteEffects = []string{
	"Their account and progress are deleted now. Their web sessions and CLI tokens go with it, so they are signed out everywhere.",
	"If they have an email address, one goodbye email is queued and sent once that email exists. Other unsent emails to them are discarded.",
	"You cannot undo this here. A restore from backup is a manual job, possible for 30 days.",
	"Not done yet: the 7-day retry wait for an open exam and the cleanup of lab machines arrive with the exam work later.",
	"The audit log keeps your reason and the account id, not their name or email.",
}

func (l *lookup) deleteData(r *http.Request, st LearnerStatus) confirmData {
	return confirmData{Title: "Delete", CSRF: csrfFrom(r.Context()), ID: st.ID, Username: st.Username,
		Verb: "Delete", Path: "delete", Effects: deleteEffects, Form: true, Danger: true, Typed: true}
}

// confirmDelete shows what deleting will do. It reads no personal data and writes nothing.
func (l *lookup) confirmDelete(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), lookupTimeout)
	defer cancel()
	st, err := l.Learners.Status(ctx, r.PathValue("id"))
	if !l.statusOK(w, r, err) {
		return
	}
	render(w, http.StatusOK, confirmPage, l.deleteData(r, st))
}

// actDelete deletes after the reason and the typed username check out, then redirects to the search
// page, which needs nothing of the deleted learner.
func (l *lookup) actDelete(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), lookupTimeout)
	defer cancel()
	st, err := l.Learners.Status(ctx, r.PathValue("id"))
	if !l.statusOK(w, r, err) {
		return
	}
	d := l.deleteData(r, st)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	reason := strings.TrimSpace(r.PostForm.Get("reason"))
	d.Reason = reason
	switch {
	case reason == "" || utf8.RuneCountInString(reason) > maxReasonLen:
		err = errBadReason
	case strings.TrimSpace(r.PostForm.Get("typed")) != st.Username:
		err = errBadTyped
	default:
		err = l.Learners.Delete(ctx, operatorFrom(r.Context()), st.ID, reason)
	}
	switch {
	case errors.Is(err, errBadReason):
		d.Error = "Write a reason of 1 to 1,000 characters. Nothing was changed."
		render(w, http.StatusBadRequest, confirmPage, d)
	case errors.Is(err, errBadTyped):
		d.Error = "Type the username exactly as shown. Nothing was changed."
		render(w, http.StatusBadRequest, confirmPage, d)
	case errors.Is(err, ErrNotFound):
		http.NotFound(w, r)
	case err != nil:
		slog.Error("learner delete failed", "route", r.Pattern, "error", oneLine(err))
		http.Error(w, "delete failed", http.StatusInternalServerError)
	default:
		http.Redirect(w, r, "/learners?deleted=1", http.StatusSeeOther)
	}
}
