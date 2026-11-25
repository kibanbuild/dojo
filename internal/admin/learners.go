package admin

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Search fields of A2 (design 3.7). The field is the audit trail of a search: the query never is.
const (
	FieldUsername    = "username"
	FieldGithubLogin = "github_login"
	FieldEmail       = "email"

	// SearchAction and ViewAction are the admin_audit actions of A2 reading.
	SearchAction = "learner.search"
	ViewAction   = "learner.view"

	// SuspendAction and UnsuspendAction are the admin_audit actions of the A2 writes.
	SuspendAction   = "learner.suspend"
	UnsuspendAction = "learner.unsuspend"

	historyLimit   = 20
	maxQueryLen    = 254
	minPrefixLen   = 2
	maxReasonLen   = 1000
	searchLimit    = 25
	recentTickets  = 10
	searchAuditTyp = "learner_search"
)

var (
	errBadSearch = errors.New("invalid search")
	uuidShape    = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// LearnerSummary is one row of the search results.
type LearnerSummary struct {
	ID          string
	Username    string
	GithubLogin string
	Suspended   bool
}

// RoleProgress is a learner's passed tickets in one role.
type RoleProgress struct {
	Title  string
	Passed int
	Total  int
}

// TicketProgress is one recent ticket row. The last check's output is deliberately not read.
type TicketProgress struct {
	ItemID     string
	Title      string
	State      string
	LabState   string
	Checks     int
	LastCheck  string
	LastSeenAt time.Time
}

// LearnerView is everything the A2 page shows in M2.
type LearnerView struct {
	ID          string
	Username    string
	GithubLogin string
	Email       string
	Language    string
	CreatedAt   time.Time
	SuspendedAt *time.Time
	Roles       []RoleProgress
	Recent      []TicketProgress
	History     []HistoryRow
}

// HistoryRow is one admin_audit row that targets the learner.
type HistoryRow struct {
	At       time.Time
	Operator string
	Action   string
	Reason   string
}

// Label is the action as the page prints it.
func (h HistoryRow) Label() string {
	switch h.Action {
	case ViewAction:
		return "Viewed"
	case SuspendAction:
		return "Suspended"
	case UnsuspendAction:
		return "Unsuspended"
	}
	return h.Action
}

// When is the row's time as the page prints it.
func (h HistoryRow) When() string { return stamp(h.At) }

// LearnerStore is the A2 data. Search and View write their audit row in the transaction that reads.
type LearnerStore interface {
	// Status reads the minimum a confirm page needs, without an audit row. ErrNotFound for no learner.
	Status(ctx context.Context, id string) (LearnerStatus, error)
	// SetSuspended suspends or unsuspends the learner and writes the audit row in one transaction.
	// ErrNotFound, ErrAlreadyInState and errBadReason write nothing.
	SetSuspended(ctx context.Context, op Operator, id string, suspend bool, reason string) error
	// Search returns up to searchLimit learners matching query on field and audits learner.search with
	// the field only. more is true when there were further matches.
	Search(ctx context.Context, op Operator, field, query, reason string) (found []LearnerSummary, more bool, err error)
	// View returns the learner and audits learner.view naming it; ErrNotFound writes nothing.
	View(ctx context.Context, op Operator, id, reason string) (LearnerView, error)
}

// PGLearners is the LearnerStore over a pgx pool connected as dojo_admin.
type PGLearners struct{ Pool *pgxpool.Pool }

// searchSQL maps each field to a fixed statement; the field never reaches the SQL text as input.
// Usernames and logins match by prefix, an email only exactly and case-insensitively: no substring
// or prefix over addresses, so a search cannot be used to enumerate them.
var searchSQL = map[string]string{
	FieldUsername: `SELECT id::text, username, github_login, suspended_at IS NOT NULL FROM learners
		WHERE username LIKE $1 ESCAPE '\' ORDER BY username LIMIT $2`,
	FieldGithubLogin: `SELECT id::text, username, github_login, suspended_at IS NOT NULL FROM learners
		WHERE lower(github_login) LIKE $1 ESCAPE '\' ORDER BY username LIMIT $2`,
	FieldEmail: `SELECT id::text, username, github_login, suspended_at IS NOT NULL FROM learners
		WHERE lower(email) = $1 ORDER BY username LIMIT $2`,
}

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// pattern returns the bind value for field: a LIKE prefix pattern, or the exact lowercased email.
func pattern(field, query string) string {
	q := strings.ToLower(query)
	if field == FieldEmail {
		return q
	}
	return likeEscaper.Replace(q) + "%"
}

// checkSearch validates a search before anything is audited or queried.
func checkSearch(field, query, reason string) (string, error) {
	query = strings.TrimSpace(query)
	if _, ok := searchSQL[field]; !ok {
		return "", fmt.Errorf("%w: unknown field", errBadSearch)
	}
	n := utf8.RuneCountInString(query)
	switch {
	case n == 0 || n > maxQueryLen:
		return "", fmt.Errorf("%w: query length", errBadSearch)
	case field != FieldEmail && n < minPrefixLen:
		return "", fmt.Errorf("%w: prefix too short", errBadSearch)
	case utf8.RuneCountInString(reason) > maxReasonLen:
		return "", fmt.Errorf("%w: reason too long", errBadSearch)
	}
	return query, nil
}

// Search implements LearnerStore.
func (s PGLearners) Search(ctx context.Context, op Operator, field, query, reason string) ([]LearnerSummary, bool, error) {
	query, err := checkSearch(field, query, reason)
	if err != nil {
		return nil, false, err
	}
	var found []LearnerSummary
	err = WithAudit(ctx, s.Pool, op, AuditEntry{
		Action: SearchAction, TargetType: searchAuditTyp, TargetID: field, Reason: reason,
		After: map[string]any{"field": field},
	}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, searchSQL[field], pattern(field, query), searchLimit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var l LearnerSummary
			if err := rows.Scan(&l.ID, &l.Username, &l.GithubLogin, &l.Suspended); err != nil {
				return err
			}
			found = append(found, l)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, false, err
	}
	more := len(found) > searchLimit
	if more {
		found = found[:searchLimit]
	}
	return found, more, nil
}

// View implements LearnerStore.
func (s PGLearners) View(ctx context.Context, op Operator, id, reason string) (LearnerView, error) {
	if !uuidShape.MatchString(id) {
		return LearnerView{}, ErrNotFound
	}
	if utf8.RuneCountInString(reason) > maxReasonLen {
		return LearnerView{}, fmt.Errorf("%w: reason too long", errBadSearch)
	}
	var v LearnerView
	err := WithAudit(ctx, s.Pool, op, AuditEntry{
		Action: ViewAction, TargetType: "learner", TargetID: id, Reason: reason,
	}, func(tx pgx.Tx) error {
		var email *string
		err := tx.QueryRow(ctx, `SELECT id::text, username, github_login, email, language, created_at, suspended_at
			FROM learners WHERE id = $1::uuid`, id).
			Scan(&v.ID, &v.Username, &v.GithubLogin, &email, &v.Language, &v.CreatedAt, &v.SuspendedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if email != nil {
			v.Email = *email
		}
		if err := s.progress(ctx, tx, &v); err != nil {
			return err
		}
		return history(ctx, tx, &v)
	})
	if err != nil {
		return LearnerView{}, err
	}
	return v, nil
}

// history reads the newest audit rows that target the learner. The row of this very view is written
// after, so it appears from the next one.
func history(ctx context.Context, tx pgx.Tx, v *LearnerView) error {
	rows, err := tx.Query(ctx, `SELECT at, operator_login, action, coalesce(reason, '') FROM admin_audit
		WHERE target_type = 'learner' AND target_id = $1 ORDER BY at DESC, id DESC LIMIT $2`, v.ID, historyLimit)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var h HistoryRow
		if err := rows.Scan(&h.At, &h.Operator, &h.Action, &h.Reason); err != nil {
			return err
		}
		v.History = append(v.History, h)
	}
	return rows.Err()
}

func (s PGLearners) progress(ctx context.Context, tx pgx.Tx, v *LearnerView) error {
	roles, err := tx.Query(ctx, `SELECT r.title,
			count(*) FILTER (WHERE tp.state = 'passed'),
			count(*)
		FROM roles r
		JOIN catalog_items ci ON ci.role_id = r.id AND ci.kind = 'ticket'
		LEFT JOIN ticket_progress tp ON tp.item_id = ci.id AND tp.learner_id = $1::uuid
		GROUP BY r.id, r.title, r.kind
		ORDER BY (r.kind = 'foundations') DESC, r.title`, v.ID)
	if err != nil {
		return err
	}
	for roles.Next() {
		var p RoleProgress
		if err := roles.Scan(&p.Title, &p.Passed, &p.Total); err != nil {
			roles.Close()
			return err
		}
		v.Roles = append(v.Roles, p)
	}
	roles.Close()
	if err := roles.Err(); err != nil {
		return err
	}

	rows, err := tx.Query(ctx, `SELECT tp.item_id, ci.title, tp.state, coalesce(tp.lab_state, ''), tp.check_count,
			CASE WHEN tp.last_check_total IS NULL THEN ''
			     ELSE tp.last_check_passed::text || ' of ' || tp.last_check_total::text END,
			tp.last_seen_at
		FROM ticket_progress tp JOIN catalog_items ci ON ci.id = tp.item_id
		WHERE tp.learner_id = $1::uuid ORDER BY tp.last_seen_at DESC LIMIT $2`, v.ID, recentTickets)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var t TicketProgress
		if err := rows.Scan(&t.ItemID, &t.Title, &t.State, &t.LabState, &t.Checks, &t.LastCheck, &t.LastSeenAt); err != nil {
			return err
		}
		v.Recent = append(v.Recent, t)
	}
	return rows.Err()
}

// lookup serves the A2 pages.
type lookup struct{ Learners LearnerStore }

const lookupTimeout = 10 * time.Second

var layoutTemplates = `{{define "top"}}<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>{{.Title}} - Dojo operator</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
<link rel="stylesheet" href="` + cssHref + `"></head>
<body>{{end}}
{{define "brand"}}<a class="brand" href="/"><svg width="28" height="28" viewBox="0 0 64 64" aria-hidden="true"><polygon class="logo-ink" points="29.40,30.01 16.82,43.68 22.87,47.91 32.30,33.88"/><polygon class="logo-ink" points="35.69,30.01 48.27,43.80 41.86,47.91 32.79,33.88"/><polygon class="logo-ink" points="4.00,21.54 60.00,21.54 60.00,30.49 4.00,30.49"/><polygon class="logo-hit" points="23.23,19.97 38.11,16.10 41.98,32.07 26.86,35.69"/></svg>dojo · admin</a>
<span class="env">` + Host + ` · internal</span>{{end}}
{{define "note"}}<p class="note">Internal tool, only on this computer through the private tunnel. Everything you do here is logged with your reason.</p>{{end}}
{{define "head"}}{{template "top" .}}<header class="bar"><div class="bar-in"><div class="bar-l">{{template "brand"}}
<nav class="nav" aria-label="Operator tools"><a href="/learners" aria-current="page">Learners</a></nav></div>
<form class="signout" method="post" action="/signout"><input type="hidden" name="csrf_token" value="{{.CSRF}}"><button type="submit">Sign out</button></form></div>{{template "note"}}</header>
<main>{{end}}
{{define "foot"}}</main></body></html>{{end}}`

var searchPage = template.Must(template.New("search").Parse(layoutTemplates + `{{template "head" .}}
<h1>Learners</h1>
<form class="stack" role="search" method="get" action="/learners">
<div class="field"><label for="field">Search by</label>
<select id="field" name="field">
{{range .Fields}}<option value="{{.Value}}"{{if eq .Value $.Field}} selected{{end}}>{{.Label}}</option>
{{end}}</select></div>
<div class="field"><label for="q">Find a learner</label>
<input id="q" type="search" name="q" value="{{.Query}}" maxlength="254" autocomplete="off" required></div>
<div class="field"><label for="reason">Reason (optional)</label>
<input id="reason" type="text" name="reason" value="{{.Reason}}" maxlength="1000" autocomplete="off"></div>
<p class="hint">Username and GitHub login match by prefix, at least 2 characters. Email matches the whole address. The search is logged by field, never by what you typed; do not put personal data in the reason.</p>
<div class="row"><button class="btn btn-primary" type="submit">Search</button></div>
</form>
{{if .Error}}<p class="alert" role="alert">{{.Error}}</p>{{end}}
{{if .Searched}}{{if .Results}}
<div class="table-wrap" role="region" aria-label="Matches" tabindex="0"><table><caption>Matches</caption>
<thead><tr><th scope="col">Username</th><th scope="col">GitHub login</th><th scope="col">Account</th></tr></thead>
<tbody>
{{range .Results}}<tr><td><a href="{{.Href}}">{{.Username}}</a></td><td>{{.GithubLogin}}</td><td>{{if .Suspended}}<span class="pill pill-bad">Suspended</span>{{else}}<span class="pill pill-ok">Active</span>{{end}}</td></tr>
{{end}}</tbody></table></div>
{{if .More}}<p class="hint">More learners match. Narrow the search.</p>{{end}}
{{else if not .Error}}<p>No learner found.</p>{{end}}{{end}}
{{template "foot" .}}`))

// learnerPage links each action to its confirm page; later actions add their links to the Actions section.
var learnerPage = template.Must(template.New("learner").Parse(layoutTemplates + `{{template "head" .}}
<p class="back"><a href="/learners">Back to search</a></p>
<div><h1>@{{.V.Username}}</h1>
<p class="lede">GitHub {{.V.GithubLogin}} · {{if .V.Email}}{{.V.Email}}{{else}}no email{{end}} · joined {{.Joined}}</p></div>
<section class="card" aria-labelledby="identity"><h2 id="identity">Account</h2>
<dl>
<dt>Username</dt><dd>{{.V.Username}}</dd>
<dt>GitHub login</dt><dd>{{.V.GithubLogin}}</dd>
<dt>Email</dt><dd>{{if .V.Email}}{{.V.Email}}{{else}}None{{end}}</dd>
<dt>Language</dt><dd>{{.V.Language}}</dd>
<dt>Created</dt><dd>{{.Joined}}</dd>
<dt>Status</dt><dd>{{if .V.SuspendedAt}}<span class="pill pill-bad">Suspended since {{.Suspended}}</span>{{else}}<span class="pill pill-ok">Active</span>{{end}}</dd>
</dl></section>
<section class="card" aria-labelledby="actions"><h2 id="actions">Actions</h2>
<div class="actions">{{if .V.SuspendedAt}}<a class="btn btn-primary" href="/learners/{{.V.ID}}/unsuspend">Unsuspend this account</a>
{{else}}<a class="btn btn-quiet-danger" href="/learners/{{.V.ID}}/suspend">Suspend this account</a>
{{end}}</div></section>
<section class="card" aria-labelledby="progress"><h2 id="progress">Progress</h2>
{{if .V.Roles}}<div class="table-wrap" role="region" aria-label="Tickets passed by role" tabindex="0"><table><caption>Tickets passed by role</caption>
<thead><tr><th scope="col">Role</th><th scope="col">Passed</th></tr></thead>
<tbody>{{range .V.Roles}}<tr><td>{{.Title}}</td><td>{{.Passed}} of {{.Total}}</td></tr>
{{end}}</tbody></table></div>{{else}}<p>No catalog loaded.</p>{{end}}
<h3>Recent tickets</h3>
{{if .V.Recent}}<div class="table-wrap" role="region" aria-label="Recent tickets" tabindex="0"><table>
<thead><tr><th scope="col">Ticket</th><th scope="col">State</th><th scope="col">Lab</th><th scope="col">Checks</th><th scope="col">Last check</th><th scope="col">Last seen</th></tr></thead>
<tbody>{{range .V.Recent}}<tr><td>{{.Title}} ({{.ItemID}})</td><td>{{.State}}</td><td>{{if .LabState}}{{.LabState}}{{else}}-{{end}}</td><td>{{.Checks}}</td><td>{{if .LastCheck}}{{.LastCheck}}{{else}}-{{end}}</td><td>{{.Seen}}</td></tr>
{{end}}</tbody></table></div>{{else}}<p>No tickets started yet.</p>{{end}}
</section>
<section class="card" aria-labelledby="history"><h2 id="history">Operator history</h2>
{{if .V.History}}<div class="table-wrap" role="region" aria-label="Operator history" tabindex="0"><table><caption>Latest actions on this learner, newest first</caption>
<thead><tr><th scope="col">When</th><th scope="col">Operator</th><th scope="col">Action</th><th scope="col">Reason</th></tr></thead>
<tbody>{{range .V.History}}<tr><td>{{.When}}</td><td>{{.Operator}}</td><td>{{.Label}}</td><td>{{if .Reason}}{{.Reason}}{{else}}-{{end}}</td></tr>
{{end}}</tbody></table></div>{{else}}<p>No operator has acted on this learner yet.</p>{{end}}
</section>
<section class="card" aria-labelledby="later"><h2 id="later">Not available yet</h2>
<p class="hint">Plan, comps, hosted hours, exam attempts, devices and the last doctor report.</p></section>
{{template "foot" .}}`))

type fieldOption struct{ Value, Label string }

var fieldOptions = []fieldOption{
	{FieldUsername, "Username"}, {FieldGithubLogin, "GitHub login"}, {FieldEmail, "Email"},
}

type resultRow struct {
	LearnerSummary
	Href string
}

func (l *lookup) search(w http.ResponseWriter, r *http.Request) {
	op := operatorFrom(r.Context())
	q := r.URL.Query()
	data := map[string]any{
		"Title": "Learners", "CSRF": csrfFrom(r.Context()), "Fields": fieldOptions,
		"Field": q.Get("field"), "Query": q.Get("q"), "Reason": q.Get("reason"),
	}
	if _, asked := q["q"]; !asked && q.Get("field") == "" {
		data["Field"] = FieldUsername
		render(w, http.StatusOK, searchPage, data)
		return
	}
	data["Searched"] = true
	field, reason := q.Get("field"), q.Get("reason")
	ctx, cancel := context.WithTimeout(r.Context(), lookupTimeout)
	defer cancel()
	found, more, err := l.Learners.Search(ctx, op, field, q.Get("q"), reason)
	switch {
	case errors.Is(err, errBadSearch):
		data["Error"] = "Enter a search: the field, at least 2 characters (a whole address for email) and a reason under 1,000 characters."
		render(w, http.StatusBadRequest, searchPage, data)
		return
	case err != nil:
		slog.Error("learner search failed", "error", oneLine(err))
		http.Error(w, "search failed", http.StatusInternalServerError)
		return
	}
	rows := make([]resultRow, 0, len(found))
	for _, f := range found {
		href := url.URL{Path: "/learners/" + f.ID}
		if reason != "" {
			href.RawQuery = url.Values{"reason": {reason}}.Encode()
		}
		rows = append(rows, resultRow{LearnerSummary: f, Href: href.String()})
	}
	data["Results"], data["More"] = rows, more
	render(w, http.StatusOK, searchPage, data)
}

type learnerData struct {
	Title, CSRF       string
	V                 LearnerView
	Joined, Suspended string
}

// Seen is the last-seen time as the page prints it.
func (t TicketProgress) Seen() string { return stamp(t.LastSeenAt) }

func stamp(t time.Time) string { return t.UTC().Format("2 Jan 2006 15:04 UTC") }

func (l *lookup) view(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), lookupTimeout)
	defer cancel()
	v, err := l.Learners.View(ctx, operatorFrom(r.Context()), r.PathValue("id"), r.URL.Query().Get("reason"))
	switch {
	case errors.Is(err, ErrNotFound):
		http.NotFound(w, r)
		return
	case errors.Is(err, errBadSearch):
		http.Error(w, "reason too long", http.StatusBadRequest)
		return
	case err != nil:
		slog.Error("learner view failed", "error", oneLine(err))
		http.Error(w, "view failed", http.StatusInternalServerError)
		return
	}
	d := learnerData{Title: "@" + v.Username, CSRF: csrfFrom(r.Context()), V: v, Joined: stamp(v.CreatedAt)}
	if v.SuspendedAt != nil {
		d.Suspended = stamp(*v.SuspendedAt)
	}
	render(w, http.StatusOK, learnerPage, d)
}

// oneLine keeps a logged error on one line: the error can carry operator input (go/log-injection).
func oneLine(err error) string {
	s := strings.ReplaceAll(err.Error(), "\n", " ")
	return strings.ReplaceAll(s, "\r", " ")
}
