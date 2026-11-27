package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kibanyadev/dojo/internal/server/repo"
)

const (
	// maxTelemetryBody bounds POST /v1/telemetry: a full batch is about 2 KB.
	maxTelemetryBody = 8 << 10
	// maxTelemetryEvents is the batch size cap; the CLI flushes in batches of at most this many.
	maxTelemetryEvents = 20
	// maxDurationMS is one day, the same bound as the table's CHECK.
	maxDurationMS = 86_400_000
)

// nameRE is a command or result name: lowercase, starts with a letter, at most 32 characters.
var nameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// telemetryRequest is the body of POST /v1/telemetry. The CLI build and OS are sent once for the
// batch. Unknown fields are refused so the CLI can never send more than the table keeps.
type telemetryRequest struct {
	CLIVersion string           `json:"cli_version"`
	OS         string           `json:"os"`
	Events     []telemetryEvent `json:"events"`
}

type telemetryEvent struct {
	Command    string `json:"command"`
	Result     string `json:"result"`
	DurationMS *int   `json:"duration_ms"`
}

func (q telemetryRequest) valid() bool {
	if !osRE.MatchString(q.OS) || !cliVersionRE.MatchString(q.CLIVersion) ||
		len(q.Events) < 1 || len(q.Events) > maxTelemetryEvents {
		return false
	}
	for _, e := range q.Events {
		if !nameRE.MatchString(e.Command) || !nameRE.MatchString(e.Result) ||
			e.DurationMS == nil || *e.DurationMS < 0 || *e.DurationMS > maxDurationMS {
			return false
		}
	}
	return true
}

// telemetry is POST /v1/telemetry (design 1.7, 3.3). A request with no credential is accepted and
// stored with a null learner (rate limited per IP); a cookie or bearer principal links the events
// to the learner, and a learner with usage_stats off is answered 204 with nothing stored. A cookie
// call needs the CSRF header like any cookie write (CSRF runs before this). Always 204, no body.
func (a Auth) telemetry(w http.ResponseWriter, r *http.Request) {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTelemetryBody))
	dec.DisallowUnknownFields()
	var req telemetryRequest
	if err := dec.Decode(&req); err != nil || dec.Decode(&struct{}{}) != io.EOF || !req.valid() {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}

	var orgID, learnerID pgtype.UUID
	if p, ok := principalOf(r.Context()); ok {
		if p.suspended { // a suspended learner's CLI keeps working quietly; nothing is kept
			w.WriteHeader(http.StatusNoContent)
			return
		}
		l, err := a.Store.GetLearnerByID(r.Context(), p.learner)
		switch {
		case errors.Is(err, context.Canceled):
			return
		case errors.Is(err, pgx.ErrNoRows):
			writeError(w, http.StatusUnauthorized, "unauthenticated")
			return
		case err != nil:
			slog.Error("telemetry: load learner failed", "error", err)
			writeError(w, http.StatusInternalServerError, "server_error")
			return
		}
		if !l.UsageStats {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		orgID, learnerID = l.OrgID, l.ID
	}

	events, err := json.Marshal(req.Events)
	if err != nil {
		slog.Error("telemetry: encode events failed", "error", err)
		writeError(w, http.StatusInternalServerError, "server_error")
		return
	}
	err = a.Store.InsertTelemetryEvents(context.WithoutCancel(r.Context()), repo.InsertTelemetryEventsParams{
		OrgID: orgID, LearnerID: learnerID, CliVersion: req.CLIVersion, Os: req.OS, Events: events,
	})
	if err != nil {
		slog.Error("telemetry: insert failed", "error", err)
		writeError(w, http.StatusInternalServerError, "server_error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
