package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kibanbuild/dojo/internal/catalog"
	"github.com/kibanbuild/dojo/internal/server/repo"
)

const ticketsPrefix = "/v1/tickets/"

type ticketRoute int

const (
	routeNone ticketRoute = iota
	routeTicket
	routeBundle
	routeHint
)

// parseTicketPath splits a /v1/tickets/ subtree path. A ticket id holds a slash, which a ServeMux
// wildcard cannot be followed by more segments after, so the routes are the subtree and this parser
// picks the handler: {id}, {id}/bundle and {id}/hints/{n}. The id is validated before any SQL.
func parseTicketPath(p string) (id string, route ticketRoute, n int16) {
	rest, ok := strings.CutPrefix(p, ticketsPrefix)
	if !ok {
		return "", routeNone, 0
	}
	parts := strings.Split(rest, "/")
	if len(parts) < 2 {
		return "", routeNone, 0
	}
	id = parts[0] + "/" + parts[1]
	if !catalog.ValidTicketID(id) {
		return "", routeNone, 0
	}
	switch {
	case len(parts) == 2:
		return id, routeTicket, 0
	case len(parts) == 3 && parts[2] == "bundle":
		return id, routeBundle, 0
	case len(parts) == 4 && parts[2] == "hints" && len(parts[3]) == 1 && parts[3][0] >= '1' && parts[3][0] <= '3':
		return id, routeHint, int16(parts[3][0] - '0')
	}
	return "", routeNone, 0
}

// TicketReader reads what GET /v1/tickets/{id} answers.
type TicketReader interface {
	GetTicketView(ctx context.Context, id string) (repo.GetTicketViewRow, error)
	GetTicketProgress(ctx context.Context, arg repo.GetTicketProgressParams) (repo.TicketProgress, error)
	ListHintOpens(ctx context.Context, arg repo.ListHintOpensParams) ([]int16, error)
}

// HintOpener opens one written hint.
type HintOpener interface {
	OpenHint(ctx context.Context, learner pgtype.UUID, ticket string, n int16) (string, error)
}

// Tickets serves GET /v1/tickets/{id} and POST /v1/tickets/{id}/hints/{n} (design 3.8).
type Tickets struct {
	Reader TicketReader
	Opener HintOpener
}

type ticketHint struct {
	N    int    `json:"n"`
	Text string `json:"text"`
}

type ticketProgress struct {
	State           string  `json:"state"`
	Lab             *string `json:"lab"`
	LabState        *string `json:"lab_state"`
	CheckCount      int32   `json:"check_count"`
	LastCheckOK     *bool   `json:"last_check_ok"`
	LastCheckAt     *string `json:"last_check_at"`
	LastCheckPassed *int32  `json:"last_check_passed"`
	LastCheckTotal  *int32  `json:"last_check_total"`
	LastCheckOutput *string `json:"last_check_output"`
	FirstPassedAt   *string `json:"first_passed_at"`
	PassedSeconds   *int32  `json:"passed_seconds"`
	PassedCommit    *string `json:"passed_commit"`
}

type ticketResponse struct {
	ID         string          `json:"id"`
	Title      string          `json:"title"`
	Role       *string         `json:"role"`
	EstMinutes int32           `json:"est_minutes"`
	Tags       []string        `json:"tags"`
	Brief      string          `json:"brief"`
	HintCount  int             `json:"hint_count"`
	Hints      []ticketHint    `json:"hints"`
	Progress   *ticketProgress `json:"progress"`
}

func textPtr(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

func int4Ptr(i pgtype.Int4) *int32 {
	if !i.Valid {
		return nil
	}
	return &i.Int32
}

func timePtr(t pgtype.Timestamptz) *string {
	if !t.Valid {
		return nil
	}
	s := t.Time.UTC().Format(time.RFC3339)
	return &s
}

func progressOf(p repo.TicketProgress) *ticketProgress {
	out := &ticketProgress{
		State: p.State, Lab: textPtr(p.Lab), LabState: textPtr(p.LabState), CheckCount: p.CheckCount,
		LastCheckAt: timePtr(p.LastCheckAt), LastCheckPassed: int4Ptr(p.LastCheckPassed),
		LastCheckTotal: int4Ptr(p.LastCheckTotal), LastCheckOutput: textPtr(p.LastCheckOutput),
		FirstPassedAt: timePtr(p.FirstPassedAt), PassedSeconds: int4Ptr(p.PassedSeconds), PassedCommit: textPtr(p.PassedCommit),
	}
	if p.LastCheckOk.Valid {
		out.LastCheckOK = &p.LastCheckOk.Bool
	}
	return out
}

// buildTicket assembles the L3 answer: opened hints only, or all of them once the ticket is passed.
func buildTicket(t repo.GetTicketViewRow, prog *repo.TicketProgress, opened []int16) ticketResponse {
	out := ticketResponse{
		ID: t.ID, Title: t.Title, Role: textPtr(t.Role), EstMinutes: t.EstMinutes, Tags: t.Tags,
		Brief: t.Brief.String, HintCount: len(t.Hints), Hints: []ticketHint{},
	}
	if out.Tags == nil {
		out.Tags = []string{}
	}
	all := false
	if prog != nil {
		out.Progress = progressOf(*prog)
		all = prog.State == "passed" || prog.State == "revisit"
	}
	open := make(map[int]bool, len(opened))
	for _, n := range opened {
		open[int(n)] = true
	}
	for i, text := range t.Hints {
		if all || open[i+1] {
			out.Hints = append(out.Hints, ticketHint{N: i + 1, Text: text})
		}
	}
	return out
}

// get is the GET /v1/tickets/ dispatcher: the ticket itself and its bundle.
func (a Auth) getTicketRoute(w http.ResponseWriter, r *http.Request) {
	id, route, _ := parseTicketPath(r.URL.Path)
	switch {
	case route == routeTicket && a.Tickets != nil:
		a.Tickets.read(w, r, id)
	case route == routeBundle && a.Bundles != nil:
		a.Bundles.fetch(w, r)
	default:
		writeError(w, http.StatusNotFound, "not_found")
	}
}

// postTicketRoute is the POST /v1/tickets/ dispatcher: opening a hint.
func (a Auth) postTicketRoute(w http.ResponseWriter, r *http.Request) {
	id, route, n := parseTicketPath(r.URL.Path)
	if route != routeHint || a.Tickets == nil {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	a.Tickets.openHint(w, r, id, n)
}

func (t *Tickets) read(w http.ResponseWriter, r *http.Request, id string) {
	p, _ := principalOf(r.Context())
	view, err := t.Reader.GetTicketView(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		readFailed(w, r, err)
		return
	}
	var prog *repo.TicketProgress
	row, err := t.Reader.GetTicketProgress(r.Context(), repo.GetTicketProgressParams{LearnerID: p.learner, ItemID: id})
	switch {
	case err == nil:
		prog = &row
	case !errors.Is(err, pgx.ErrNoRows):
		readFailed(w, r, err)
		return
	}
	opened, err := t.Reader.ListHintOpens(r.Context(), repo.ListHintOpensParams{LearnerID: p.learner, ItemID: id})
	if err != nil {
		readFailed(w, r, err)
		return
	}
	writeNoStore(w, buildTicket(view, prog, opened))
}

func (t *Tickets) openHint(w http.ResponseWriter, r *http.Request, id string, n int16) {
	p, _ := principalOf(r.Context())
	text, err := t.Opener.OpenHint(context.WithoutCancel(r.Context()), p.learner, id, n)
	switch {
	case errors.Is(err, repo.ErrTicketNotFound):
		writeError(w, http.StatusNotFound, "not_found")
	case errors.Is(err, repo.ErrHintLocked):
		writeError(w, http.StatusConflict, "hint_locked")
	case errors.Is(err, repo.ErrLearnerGone):
		writeError(w, http.StatusUnauthorized, "unauthenticated")
	case err != nil:
		slog.Error("hint open failed", "route", r.Pattern, "error", err)
		writeError(w, http.StatusInternalServerError, "server_error")
	default:
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, ticketHint{N: int(n), Text: text})
	}
}
