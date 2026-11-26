package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kibanbuild/dojo/internal/server/repo"
)

func (f *fakeCatalog) ListLearnerActivityDays(_ context.Context, a repo.ListLearnerActivityDaysParams) ([]repo.ListLearnerActivityDaysRow, error) {
	f.gotTZ = a.Tz
	return f.days, f.err
}

func (f *fakeCatalog) CountLearnerPasses(context.Context, pgtype.UUID) (int32, error) {
	return f.total, f.err
}

func (f *fakeCatalog) ListLearnerRecentPasses(context.Context, pgtype.UUID) ([]repo.ListLearnerRecentPassesRow, error) {
	return f.recent, f.err
}

func TestActivityShape(t *testing.T) {
	at := time.Date(2026, 11, 24, 21, 30, 0, 0, time.UTC)
	f := &fakeCatalog{
		days:  []repo.ListLearnerActivityDaysRow{{Day: pgtype.Date{Time: time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC), Valid: true}, Passes: 2}},
		total: 7,
		recent: []repo.ListLearnerRecentPassesRow{
			{ItemID: "foundations/a", Title: "A", FirstPassedAt: pgtype.Timestamptz{Time: at, Valid: true}},
			{ItemID: "support-l1/c", Title: "C", FirstPassedAt: pgtype.Timestamptz{Time: at, Valid: true}, PassedCommit: pgtype.Text{String: "abc123", Valid: true}, CommitState: pgtype.Text{String: "verified", Valid: true}},
			{ItemID: "support-l1/d", Title: "D", FirstPassedAt: pgtype.Timestamptz{Time: at, Valid: true}, PassedCommit: pgtype.Text{String: "def456", Valid: true}, CommitState: pgtype.Text{String: "pending", Valid: true}},
		},
	}
	rec := getCatalogRoute(t, f, "/v1/activity", false, false)
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var got struct {
		From, To string
		Days     []map[string]any
		Total    int
		Recent   []map[string]any
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Days) != 1 || got.Days[0]["day"] != "2026-11-24" || got.Days[0]["count"] != float64(2) || got.Total != 7 {
		t.Fatalf("days/total: %s", rec.Body)
	}
	if len(got.Recent) != 3 || got.Recent[0]["commit"] != nil || got.Recent[1]["commit"] != "abc123" || got.Recent[2]["commit"] != nil ||
		got.Recent[0]["at"] != "2026-11-24T21:30:00Z" || got.Recent[0]["ticket_id"] != "foundations/a" || got.Recent[0]["title"] != "A" {
		t.Fatalf("recent: %s", rec.Body)
	}
	from, _ := time.Parse(dayLayout, got.From)
	to, _ := time.Parse(dayLayout, got.To)
	if to.Sub(from) != 139*24*time.Hour || f.gotTZ != "UTC" {
		t.Fatalf("window %s..%s tz %q", got.From, got.To, f.gotTZ)
	}
}

func TestActivityEmpty(t *testing.T) {
	rec := getCatalogRoute(t, &fakeCatalog{}, "/v1/activity", false, false)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"days":[],"total":0,"recent":[]`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestActivityTZ(t *testing.T) {
	for _, tt := range []struct {
		query string
		code  int
		tz    string
	}{
		{"", 200, "UTC"},
		{"?tz=Europe/Madrid", 200, "Europe/Madrid"},
		{"?tz=America/Argentina/Buenos_Aires", 200, "America/Argentina/Buenos_Aires"},
		{"?tz=Mars/Base", 400, ""},
		{"?tz=Local", 400, ""},
		{"?tz=" + strings.Repeat("a", 65), 400, ""},
		{"?tz=..%2F..%2Fetc%2Fpasswd", 400, ""},
	} {
		f := &fakeCatalog{}
		rec := getCatalogRoute(t, f, "/v1/activity"+tt.query, false, false)
		if rec.Code != tt.code || f.gotTZ != tt.tz {
			t.Errorf("%q: %d tz %q %s", tt.query, rec.Code, f.gotTZ, rec.Body)
		}
		if tt.code == 400 && rec.Body.String() != "{\"error\":\"bad_request\"}\n" {
			t.Errorf("%q: %s", tt.query, rec.Body)
		}
	}
}

func TestActivityRefusals(t *testing.T) {
	if rec := getCatalogRoute(t, &fakeCatalog{}, "/v1/activity", false, true); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", rec.Code)
	}
	rec := getCatalogRoute(t, &fakeCatalog{}, "/v1/activity", true, false)
	if rec.Code != http.StatusForbidden || rec.Body.String() != "{\"error\":\"account_suspended\"}\n" {
		t.Fatalf("suspended: %d %s", rec.Code, rec.Body)
	}
}

func TestActivityReadFailure(t *testing.T) {
	if rec := getCatalogRoute(t, &fakeCatalog{err: context.DeadlineExceeded}, "/v1/activity", false, false); rec.Code != http.StatusInternalServerError {
		t.Fatalf("%d", rec.Code)
	}
}

func TestActivityPostgresRejectsTZ(t *testing.T) {
	err := &pgconn.PgError{Code: "22023", Message: `time zone "posixrules" not recognized`}
	rec := getCatalogRoute(t, &fakeCatalog{err: fmt.Errorf("query: %w", err)}, "/v1/activity?tz=posixrules", false, false)
	if rec.Code != http.StatusBadRequest || rec.Body.String() != "{\"error\":\"bad_request\"}\n" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	other := &pgconn.PgError{Code: "40001"}
	if rec := getCatalogRoute(t, &fakeCatalog{err: other}, "/v1/activity", false, false); rec.Code != http.StatusInternalServerError {
		t.Fatalf("other pg error: %d", rec.Code)
	}
}

func TestActivityWindow(t *testing.T) {
	madrid, _ := time.LoadLocation("Europe/Madrid")
	now := time.Date(2026, 11, 24, 23, 30, 0, 0, time.UTC) // already 00:30 on the 25th in Madrid
	from, to, since := activityWindow(now, time.UTC)
	if to.Format(dayLayout) != "2026-11-24" || from.Format(dayLayout) != "2026-07-08" || !since.Equal(time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("utc %v %v %v", from, to, since)
	}
	from, to, since = activityWindow(now, madrid)
	if to.Format(dayLayout) != "2026-11-25" || from.Format(dayLayout) != "2026-07-09" || !since.Equal(time.Date(2026, 7, 8, 22, 0, 0, 0, time.UTC)) {
		t.Fatalf("madrid %v %v %v", from, to, since)
	}
}
