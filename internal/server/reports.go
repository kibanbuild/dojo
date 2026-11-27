package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/kibanyadev/dojo/internal/server/repo"
)

const (
	reportsPath      = "/v1/reports"
	maxReportBody    = 8 << 10
	maxReportMessage = 2000
)

// ReportStore stores a learner's report (design 3.9, "The report form").
type ReportStore interface {
	CreateReport(ctx context.Context, in repo.ReportIn) (repo.ReportResult, error)
}

var reportTicketRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*/[a-z0-9]+(-[a-z0-9]+)*$`)

// reportRequest is the body of POST /v1/reports. Unknown fields are refused.
type reportRequest struct {
	Kind          string  `json:"kind"`
	ItemID        *string `json:"item_id"`
	Message       string  `json:"message"`
	IncludeOutput bool    `json:"include_output"`
}

// validate returns the report to store, or the error name to answer with: bad_request (400) or not_found (404,
// an item id that cannot be a ticket). Whether the ticket exists is the store's to say.
func (r reportRequest) validate() (repo.ReportIn, string) {
	in := repo.ReportIn{Kind: r.Kind, IncludeOutput: r.IncludeOutput}
	if r.Kind != "broken_ticket" && r.Kind != "setup_help" {
		return in, "bad_request"
	}
	if r.ItemID != nil {
		in.ItemID = *r.ItemID
	}
	if in.ItemID == "" && r.Kind == "broken_ticket" {
		return in, "bad_request"
	}
	if in.ItemID != "" && !reportTicketRE.MatchString(in.ItemID) {
		return in, "not_found"
	}
	msg := strings.TrimSpace(r.Message)
	if n := utf8.RuneCountInString(msg); n < 1 || n > maxReportMessage || strings.ContainsRune(msg, 0) || !utf8.ValidString(msg) {
		return in, "bad_request"
	}
	in.Message = msg
	return in, ""
}

// postReport serves POST /v1/reports: it stores the report and answers 201 with its id. The mail to us goes
// out later, from the reader (reports_mail.go), so a mail outage never fails the learner's request.
func (a Auth) postReport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxReportBody))
	dec.DisallowUnknownFields()
	var req reportRequest
	if err := dec.Decode(&req); err != nil || dec.Decode(&struct{}{}) != io.EOF {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	in, name := req.validate()
	switch name {
	case "bad_request":
		writeError(w, http.StatusBadRequest, name)
		return
	case "not_found":
		writeError(w, http.StatusNotFound, name)
		return
	}
	in.LearnerID = principalLearner(r)
	res, err := a.Reports.CreateReport(context.WithoutCancel(r.Context()), in)
	switch {
	case errors.Is(err, repo.ErrUnknownItem):
		writeError(w, http.StatusNotFound, "not_found")
	case errors.Is(err, pgx.ErrNoRows): // the learner was deleted since the session lookup
		writeError(w, http.StatusUnauthorized, "unauthenticated")
	case err != nil:
		slog.Error("create report failed", "error", err)
		writeError(w, http.StatusInternalServerError, "server_error")
	case res.RetryAfter > 0:
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(res.RetryAfter.Seconds())))))
		writeError(w, http.StatusTooManyRequests, "rate_limited")
	default:
		writeJSON(w, http.StatusCreated, map[string]string{"id": res.ID.String()})
	}
}
