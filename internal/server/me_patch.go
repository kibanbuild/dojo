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
	// UsageStats is the CLI and web usage-stats switch (design 1.7); a JSON null is absent.
	UsageStats *bool `json:"usage_stats"`
	// AnalyticsConsent is the web analytics choice (design section on web analytics); a JSON null is absent.
	AnalyticsConsent *bool `json:"analytics_consent"`
	// HostedDefault is "use a hosted lab" (design 3.9); a JSON null is absent.
	HostedDefault *bool `json:"hosted_default"`
}

func validLanguage(l string) bool { return l == "en" || l == "es" }

// patchMe updates the signed-in learner's username, language, usage-stats switch and analytics consent (S1) and answers with the
// same body as GET /v1/me. The username is lowercased first, then held to the sign-in rules.
func (a Auth) patchMe(w http.ResponseWriter, r *http.Request) {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPatchBody))
	dec.DisallowUnknownFields()
	var req patchMeRequest
	if err := dec.Decode(&req); err != nil || dec.Decode(&struct{}{}) != io.EOF || (req.Username == nil && req.Language == nil && req.UsageStats == nil && req.AnalyticsConsent == nil && req.HostedDefault == nil) {
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

	if req.UsageStats != nil {
		arg.UsageStats = pgtype.Bool{Bool: *req.UsageStats, Valid: true}
	}
	if req.AnalyticsConsent != nil {
		arg.AnalyticsConsent = pgtype.Bool{Bool: *req.AnalyticsConsent, Valid: true}
	}
	if req.HostedDefault != nil {
		arg.HostedDefault = pgtype.Bool{Bool: *req.HostedDefault, Valid: true}
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
