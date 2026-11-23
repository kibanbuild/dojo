package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
)

// maxDeleteBody bounds DELETE /v1/me: one short login.
const maxDeleteBody = 1 << 10

type deleteMeRequest struct {
	Confirm *string `json:"confirm"`
}

// deleteMe erases the signed-in learner's account (S1, GDPR erasure). Only the cookie session can: a
// CLI token is valid but must not delete an account, so it gets 403 forbidden (a 401 would make the CLI
// ask for a new login). A suspended learner may delete their own account. The body must repeat the
// learner's GitHub login, compared case-insensitively. Nothing personal is logged.
func (a Auth) deleteMe(w http.ResponseWriter, r *http.Request) {
	p, _ := principalOf(r.Context())
	if p.bearer {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxDeleteBody))
	dec.DisallowUnknownFields()
	var req deleteMeRequest
	if err := dec.Decode(&req); err != nil || dec.Decode(&struct{}{}) != io.EOF || req.Confirm == nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	ctx := context.WithoutCancel(r.Context())
	l, err := a.Store.GetLearnerByID(ctx, p.learner)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	case err != nil:
		slog.Error("delete account: loading the learner failed", "error", err)
		writeError(w, http.StatusInternalServerError, "server_error")
		return
	}
	if !strings.EqualFold(*req.Confirm, l.GithubLogin) {
		writeError(w, http.StatusBadRequest, "confirm_mismatch")
		return
	}
	deleted, err := a.Store.DeleteLearner(ctx, p.learner)
	if err != nil {
		slog.Error("delete account failed", "error", err)
		writeError(w, http.StatusInternalServerError, "server_error")
		return
	}
	if !deleted {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	http.SetCookie(w, a.ClearCookie())
	w.WriteHeader(http.StatusNoContent)
}
