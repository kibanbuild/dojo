package server

import (
	"errors"
	"net/http"
	"time"
	_ "time/tzdata" // the api image is scratch with no zoneinfo, so tz names resolve from this copy

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kibanbuild/dojo/internal/server/repo"
)

const (
	activityDays = 140 // 20 weeks, the window L1 draws
	maxTZLen     = 64
	dayLayout    = "2006-01-02"
)

type activityDay struct {
	Day   string `json:"day"`
	Count int    `json:"count"`
}

type activityRecent struct {
	TicketID string  `json:"ticket_id"`
	Title    string  `json:"title"`
	At       string  `json:"at"`
	Commit   *string `json:"commit"`
}

type activityResponse struct {
	From   string           `json:"from"`
	To     string           `json:"to"`
	Days   []activityDay    `json:"days"`
	Total  int              `json:"total"`
	Recent []activityRecent `json:"recent"`
}

// parseTZ reads the optional tz query parameter: an IANA name, UTC when absent.
func parseTZ(r *http.Request) (*time.Location, bool) {
	name := r.URL.Query().Get("tz")
	if name == "" {
		return time.UTC, true
	}
	// "Local" would resolve to the server's zone, and an empty name is handled above.
	if len(name) > maxTZLen || name == "Local" {
		return nil, false
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, false
	}
	return loc, true
}

// activityWindow is the 140 days ending on the calendar day of now in loc: the first and last day
// and the instant the first day starts.
func activityWindow(now time.Time, loc *time.Location) (from, to time.Time, since time.Time) {
	y, m, d := now.In(loc).Date()
	to = time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	from = to.AddDate(0, 0, -(activityDays - 1))
	since = time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc)
	return from, to, since
}

// activity answers what the L1 Today activity panel draws: first passes per day over the last 140
// days, the all-time total and the latest three passes (design 3.8).
func (a Auth) activity(w http.ResponseWriter, r *http.Request) {
	loc, ok := parseTZ(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	p, _ := principalOf(r.Context())
	c, s := a.Catalog, r.Context()
	from, to, since := activityWindow(time.Now(), loc)
	days, err1 := c.ListLearnerActivityDays(s, repo.ListLearnerActivityDaysParams{
		Tz: loc.String(), LearnerID: p.learner, Since: pgtype.Timestamptz{Time: since, Valid: true}})
	total, err2 := c.CountLearnerPasses(s, p.learner)
	recent, err3 := c.ListLearnerRecentPasses(s, p.learner)
	if err := errors.Join(err1, err2, err3); err != nil {
		// A name Go accepts but Postgres rejects (invalid_parameter_value): the caller's input, and
		// the error text echoes it, so it is neither logged nor a 500.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "22023" {
			writeError(w, http.StatusBadRequest, "bad_request")
			return
		}
		readFailed(w, r, err)
		return
	}
	out := activityResponse{From: from.Format(dayLayout), To: to.Format(dayLayout),
		Days: []activityDay{}, Total: int(total), Recent: []activityRecent{}}
	for _, d := range days {
		out.Days = append(out.Days, activityDay{d.Day.Time.Format(dayLayout), int(d.Passes)})
	}
	for _, x := range recent {
		rc := activityRecent{TicketID: x.ItemID, Title: x.Title, At: x.FirstPassedAt.Time.UTC().Format(time.RFC3339)}
		if x.PassedCommit.Valid {
			rc.Commit = &x.PassedCommit.String
		}
		out.Recent = append(out.Recent, rc)
	}
	writeNoStore(w, out)
}
