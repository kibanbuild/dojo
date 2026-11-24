package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kibanbuild/dojo/internal/server/repo"
)

const (
	eventsPath     = "/v1/events"
	maxEventsBody  = 1 << 20
	maxBatchEvents = 50
	maxEventBytes  = 64 << 10
	maxOutputBytes = 16384
	maxDetailChars = 512
	maxCheckCount  = 1000
)

var (
	eventIDRE      = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	ticketIDRE     = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*/[a-z0-9]+(-[a-z0-9]+)*$`)
	catalogVerRE   = regexp.MustCompile(`^[0-9a-f]{12}$`)
	labFailReasons = map[string]bool{"disk": true, "memory": true, "vm": true, "setup": true, "other": true}
)

// EventStore writes a validated batch and its progress projection.
type EventStore interface {
	RecordEvents(ctx context.Context, learner pgtype.UUID, evs []repo.EventIn) (repo.EventsResult, error)
}

// wireEvent is one event as sent; every field is a pointer so absence is visible.
type wireEvent struct {
	ID             *string `json:"id"`
	Type           *string `json:"type"`
	At             *string `json:"at"`
	ItemID         *string `json:"item_id"`
	Lab            *string `json:"lab"`
	CatalogVersion *string `json:"catalog_version"`
	Reason         *string `json:"reason"`
	Detail         *string `json:"detail"`
	Result         *string `json:"result"`
	DurationMS     *int    `json:"duration_ms"`
	Passed         *int    `json:"passed"`
	Total          *int    `json:"total"`
	Output         *string `json:"output"`
}

// parseEvent decodes and validates one raw event; ok is false for any rule it breaks.
func parseEvent(raw json.RawMessage) (repo.EventIn, bool) {
	var out repo.EventIn
	if len(raw) > maxEventBytes {
		return out, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var w wireEvent
	if err := dec.Decode(&w); err != nil || dec.Decode(&struct{}{}) != io.EOF {
		return out, false
	}
	if w.ID == nil || w.Type == nil || w.At == nil || w.ItemID == nil ||
		!eventIDRE.MatchString(*w.ID) || len(*w.ItemID) < 3 || len(*w.ItemID) > 64 || !ticketIDRE.MatchString(*w.ItemID) ||
		!strings.HasSuffix(*w.At, "Z") {
		return out, false
	}
	at, err := time.Parse(time.RFC3339Nano, *w.At)
	if err != nil {
		return out, false
	}
	var id pgtype.UUID
	if id.Scan(*w.ID) != nil {
		return out, false
	}

	// present lists the optional fields the event carries; each type allows its own set only.
	present := map[string]bool{
		"lab": w.Lab != nil, "catalog_version": w.CatalogVersion != nil, "reason": w.Reason != nil,
		"detail": w.Detail != nil, "result": w.Result != nil, "duration_ms": w.DurationMS != nil,
		"passed": w.Passed != nil, "total": w.Total != nil, "output": w.Output != nil,
	}
	allowed := map[string][]string{
		"ticket.started": {"lab", "catalog_version"},
		"lab.started":    {},
		"lab.stopped":    {},
		"lab.failed":     {"reason", "detail"},
		"check.run":      {"result", "duration_ms", "passed", "total", "output"},
	}[*w.Type]
	if allowed == nil { // unknown type, and hint.opened (server side only)
		return out, false
	}
	for name, has := range present {
		if has && !contains(allowed, name) {
			return out, false
		}
	}

	e := repo.EventIn{ClientID: id, Type: *w.Type, ItemID: *w.ItemID, ClientAt: at.UTC()}
	payload := map[string]any{"item_id": *w.ItemID}
	switch *w.Type {
	case "ticket.started":
		if w.Lab == nil || (*w.Lab != "local" && *w.Lab != "hosted") || w.CatalogVersion == nil || !catalogVerRE.MatchString(*w.CatalogVersion) {
			return out, false
		}
		e.Lab = *w.Lab
		payload["lab"], payload["catalog_version"] = *w.Lab, *w.CatalogVersion
	case "lab.failed":
		if w.Reason == nil || !labFailReasons[*w.Reason] {
			return out, false
		}
		payload["reason"] = *w.Reason
		if w.Detail != nil {
			detail := noNUL(*w.Detail)
			if utf8.RuneCountInString(detail) > maxDetailChars {
				return out, false
			}
			payload["detail"] = detail
		}
	case "check.run":
		if w.Result == nil || (*w.Result != "pass" && *w.Result != "fail") ||
			w.DurationMS == nil || *w.DurationMS < 0 || *w.DurationMS > maxDurationMS {
			return out, false
		}
		if (w.Passed == nil) != (w.Total == nil) {
			return out, false
		}
		if w.Passed != nil {
			if *w.Passed < 0 || *w.Total > maxCheckCount || *w.Passed > *w.Total {
				return out, false
			}
			p, t := int32(*w.Passed), int32(*w.Total) //nolint:gosec // both within 0..1000 above
			e.Passed, e.Total = &p, &t
			payload["passed"], payload["total"] = p, t
		}
		if w.Output != nil {
			output := noNUL(*w.Output) // the cap applies after the replacement grows each NUL to 3 bytes
			if len(output) > maxOutputBytes {
				return out, false
			}
			e.Output = &output
		}
		e.Result = *w.Result
		payload["result"], payload["duration_ms"] = *w.Result, *w.DurationMS
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return out, false
	}
	e.Payload = b
	return e, true
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// noNUL replaces NUL with U+FFFD: Postgres text and jsonb cannot hold it (invalid UTF-8 is already replaced by the decoder).
func noNUL(s string) string { return strings.ReplaceAll(s, "\x00", "�") }

func badEvents(w http.ResponseWriter, index int) {
	if index < 0 {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad_request", "index": index})
}

// postEvents is POST /v1/events (design 3.8): bearer only, the whole batch validated before any write.
func (a Auth) postEvents(w http.ResponseWriter, r *http.Request) {
	p, ok := principalOf(r.Context())
	switch {
	case !ok || !p.bearer:
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	case p.suspended:
		writeError(w, http.StatusForbidden, "account_suspended")
		return
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		badEvents(w, -1)
		return
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxEventsBody))
	dec.DisallowUnknownFields()
	var req struct {
		Events []json.RawMessage `json:"events"`
	}
	if err := dec.Decode(&req); err != nil || dec.Decode(&struct{}{}) != io.EOF ||
		len(req.Events) < 1 || len(req.Events) > maxBatchEvents {
		badEvents(w, -1)
		return
	}
	evs := make([]repo.EventIn, len(req.Events))
	for i, raw := range req.Events {
		e, ok := parseEvent(raw)
		if !ok {
			badEvents(w, i)
			return
		}
		evs[i] = e
	}

	res, err := a.Events.RecordEvents(context.WithoutCancel(r.Context()), p.learner, evs)
	switch {
	case errors.Is(err, repo.ErrLearnerGone):
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	case err != nil:
		slog.Error("events: record failed", "error", err)
		writeError(w, http.StatusInternalServerError, "server_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"accepted": res.Accepted, "duplicates": res.Duplicates, "ignored": res.Ignored})
}
