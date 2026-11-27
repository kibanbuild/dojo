package server

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kibanyadev/dojo/internal/server/repo"
)

// deviceResponse is one CLI device in GET /v1/me/devices. id is the cli_tokens row id, never
// the token or its hash. current marks the token the request itself carries (bearer calls only).
type deviceResponse struct {
	ID         string    `json:"id"`
	DeviceName string    `json:"device_name"`
	OS         string    `json:"os"`
	CLIVersion string    `json:"cli_version"`
	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at"`
	Current    bool      `json:"current"`
}

// devices lists the signed-in learner's active CLI tokens, most recently used first (S2).
func (a Auth) devices(w http.ResponseWriter, r *http.Request) {
	p, _ := principalOf(r.Context())
	rows, err := a.Store.ListLearnerCLITokens(r.Context(), p.learner)
	if err != nil {
		if r.Context().Err() == nil {
			slog.Error("list devices failed", "error", err)
			writeError(w, http.StatusInternalServerError, "server_error")
		}
		return
	}
	out := make([]deviceResponse, 0, len(rows))
	for _, d := range rows {
		out = append(out, deviceResponse{
			ID: d.ID.String(), DeviceName: d.DeviceName, OS: d.Os, CLIVersion: d.CliVersion,
			CreatedAt: d.CreatedAt.Time, LastUsedAt: d.LastUsedAt.Time,
			Current: p.bearer && d.ID == p.cliToken,
		})
	}
	writeJSON(w, http.StatusOK, map[string][]deviceResponse{"devices": out})
}

// revokeDevice ends one of the learner's own CLI tokens by id. An unknown, foreign, already
// revoked or malformed id all answer the same 404, so the route is no oracle for other
// learners' token ids. Revoking the token the call carries is allowed: it is a sign-out.
func (a Auth) revokeDevice(w http.ResponseWriter, r *http.Request) {
	p, _ := principalOf(r.Context())
	id, ok := parseDeviceID(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	n, err := a.Store.RevokeLearnerCLIToken(context.WithoutCancel(r.Context()),
		repo.RevokeLearnerCLITokenParams{ID: id, LearnerID: p.learner})
	switch {
	case err != nil:
		slog.Error("revoke device failed", "error", err)
		writeError(w, http.StatusInternalServerError, "server_error")
	case n == 0:
		writeError(w, http.StatusNotFound, "not_found")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// parseDeviceID accepts only the canonical 8-4-4-4-12 form the list returns: dashes at 8, 13, 18
// and 23, hex digits (either case) elsewhere. Other spellings Scan would take (no dashes, braces,
// underscores) are refused.
func parseDeviceID(s string) (pgtype.UUID, bool) {
	if len(s) != 36 {
		return pgtype.UUID{}, false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return pgtype.UUID{}, false
			}
		case (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F'):
			return pgtype.UUID{}, false
		}
	}
	var id pgtype.UUID
	if id.Scan(s) != nil {
		return pgtype.UUID{}, false
	}
	return id, true
}
