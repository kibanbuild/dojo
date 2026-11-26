package repo

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestReportWait(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	at := func(ago time.Duration) pgtype.Timestamptz {
		return pgtype.Timestamptz{Time: now.Add(-ago), Valid: true}
	}
	tests := []struct {
		name string
		c    CountRecentReportsRow
		want time.Duration
	}{
		{"none", CountRecentReportsRow{DbNow: at(0)}, 0},
		{"four in the hour", CountRecentReportsRow{HourCount: 4, DayCount: 4, HourOldest: at(10 * time.Minute), DayOldest: at(10 * time.Minute), DbNow: at(0)}, 0},
		{"five in the hour waits for the oldest to leave it", CountRecentReportsRow{HourCount: 5, DayCount: 5, HourOldest: at(50 * time.Minute), DayOldest: at(50 * time.Minute), DbNow: at(0)}, 10 * time.Minute},
		{"twenty in the day waits for the oldest to leave it", CountRecentReportsRow{HourCount: 1, DayCount: 20, HourOldest: at(time.Minute), DayOldest: at(23 * time.Hour), DbNow: at(0)}, time.Hour},
		{"both exceeded takes the longer", CountRecentReportsRow{HourCount: 5, DayCount: 20, HourOldest: at(10 * time.Minute), DayOldest: at(23 * time.Hour), DbNow: at(0)}, time.Hour},
		{"a window edge never answers zero", CountRecentReportsRow{HourCount: 5, DayCount: 5, HourOldest: at(time.Hour), DayOldest: at(time.Hour), DbNow: at(0)}, time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := reportWait(tt.c); got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}
