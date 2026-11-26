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
	// Email is the learner's own address, accepted only while none is on file (design 3.9).
	Email *string `json:"email"`
	// FirstRunSeen only accepts true: the learner left the first-run Today (design 3.9).
	FirstRunSeen *bool `json:"first_run_seen"`
}

func validLanguage(l string) bool { return l == "en" || l == "es" }

const (
	minEmail = 3
	maxEmail = 320
)

// normalizeEmail holds an address to the design 3.9 rules and lower-cases its domain. The local part takes
// only letters, digits and . _ % + -, so an address can never carry a header or a second recipient.
func normalizeEmail(s string) (string, bool) {
	if len(s) < minEmail || len(s) > maxEmail || strings.Count(s, "@") != 1 {
		return "", false
	}
	local, domain, _ := strings.Cut(s, "@")
	if local == "" {
		return "", false
	}
	for _, c := range local {
		if !isLocalChar(c) {
			return "", false
		}
	}
	domain = strings.ToLower(domain)
	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return "", false
	}
	for _, l := range labels {
		if l == "" {
			return "", false
		}
		for _, c := range l {
			if !isLabelChar(c) {
				return "", false
			}
		}
	}
	return local + "@" + domain, true
}

func isLabelChar(c rune) bool { return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' }

func isLocalChar(c rune) bool {
	return isLabelChar(c) || c >= 'A' && c <= 'Z' || strings.ContainsRune("._%+", c)
}

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
	if err := dec.Decode(&req); err != nil || dec.Decode(&struct{}{}) != io.EOF || (req.Username == nil && req.Language == nil && req.UsageStats == nil && req.AnalyticsConsent == nil && req.HostedDefault == nil && req.Email == nil && req.FirstRunSeen == nil) {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	if req.FirstRunSeen != nil && !*req.FirstRunSeen {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	email := ""
	if req.Email != nil {
		var ok bool
		if email, ok = normalizeEmail(*req.Email); !ok {
			writeError(w, http.StatusBadRequest, "invalid_email")
			return
		}
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

	ctx := context.WithoutCancel(r.Context())
	fail := func(what string, err error) {
		slog.Error(what+" failed", "error", err)
		writeError(w, http.StatusInternalServerError, "server_error")
	}
	// An address already on file refuses the whole request before anything is written.
	if req.Email != nil {
		cur, err := a.Store.GetLearnerByID(ctx, arg.ID)
		if err != nil {
			fail("load learner", err)
			return
		}
		if cur.Email.Valid {
			writeError(w, http.StatusConflict, "email_set")
			return
		}
	}
	var l repo.Learner
	if req.Username != nil || req.Language != nil || req.UsageStats != nil || req.AnalyticsConsent != nil || req.HostedDefault != nil {
		var err error
		l, err = a.Store.UpdateLearnerProfile(ctx, arg)
		var pg *pgconn.PgError
		switch {
		case errors.As(err, &pg) && pg.Code == "23505":
			writeError(w, http.StatusConflict, "username_taken")
			return
		case err != nil:
			fail("update learner profile", err)
			return
		}
	}
	if req.Email != nil {
		set, ok, err := a.Store.SetLearnerEmail(ctx, arg.ID, email)
		switch {
		case err != nil:
			fail("set learner email", err)
			return
		case !ok:
			writeError(w, http.StatusConflict, "email_set")
			return
		}
		l = set
	}
	if req.FirstRunSeen != nil {
		seen, err := a.Store.MarkFirstRunSeen(ctx, arg.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			seen, err = a.Store.GetLearnerByID(ctx, arg.ID)
		}
		if err != nil {
			fail("mark first run seen", err)
			return
		}
		l = seen
	}
	writeJSON(w, http.StatusOK, newMeResponse(l))
}

func principalLearner(r *http.Request) pgtype.UUID {
	p, _ := principalOf(r.Context())
	return p.learner
}
