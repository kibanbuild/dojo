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

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kibanbuild/dojo/internal/server/repo"
)

// maxPatchBody bounds PATCH /v1/me: two short fields.
const maxPatchBody = 1 << 10

// patchMeRequest is the body of PATCH /v1/me. A nil field is left as it is; unknown fields are refused.
type patchMeRequest struct {
	Username *string `json:"username"`
	Language *string `json:"language"`
}

func validLanguage(l string) bool { return l == "en" || l == "es" }

// patchMe updates the signed-in learner's username and language (S1) and answers with the
// same body as GET /v1/me. The username is lowercased first, then held to the sign-in rules.
func (a Auth) patchMe(w http.ResponseWriter, r *http.Request) {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPatchBody))
	dec.DisallowUnknownFields()
	var req patchMeRequest
	if err := dec.Decode(&req); err != nil || dec.Decode(&struct{}{}) != io.EOF || (req.Username == nil && req.Language == nil) {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	arg := repo.UpdateLearnerProfileParams{ID: principalLearner(r)}
	if req.Username != nil {
		name := strings.ToLower(*req.Username)
		if !usernameRE.MatchString(name) || reservedUsernames[name] {
			writeError(w, http.StatusBadRequest, "invalid_username")
			return
		}
		arg.Username = pgtype.Text{String: name, Valid: true}
	}
	if req.Language != nil {
		if !validLanguage(*req.Language) {
			writeError(w, http.StatusBadRequest, "invalid_language")
			return
		}
		arg.Language = pgtype.Text{String: *req.Language, Valid: true}
	}

	l, err := a.Store.UpdateLearnerProfile(context.WithoutCancel(r.Context()), arg)
	var pg *pgconn.PgError
	switch {
	case errors.As(err, &pg) && pg.Code == "23505":
		writeError(w, http.StatusConflict, "username_taken")
		return
	case err != nil:
		slog.Error("update learner profile failed", "error", err)
		writeError(w, http.StatusInternalServerError, "server_error")
		return
	}
	writeJSON(w, http.StatusOK, newMeResponse(l))
}

func principalLearner(r *http.Request) pgtype.UUID {
	p, _ := principalOf(r.Context())
	return p.learner
}
