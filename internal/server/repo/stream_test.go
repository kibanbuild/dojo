package repo

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestLearnerChannelHasNoDashes(t *testing.T) {
	id := pgtype.UUID{Bytes: [16]byte{0xab, 1}, Valid: true}
	if got := LearnerChannel(id); got != "dojo_learner_ab010000000000000000000000000000" {
		t.Fatal(got)
	}
}

func TestProgressPayload(t *testing.T) {
	p := TicketProgress{ItemID: "r/t", State: "started", CheckCount: 2, LastCheckOutput: pgtype.Text{String: "SECRET", Valid: true},
		LabState: pgtype.Text{String: "running", Valid: true}, LastCheckOk: pgtype.Bool{Bool: false, Valid: true}, LastCheckAt: ts(t0)}
	got := string(ProgressPayload(p))
	want := `{"item_id":"r/t","state":"started","lab_state":"running","lab":null,"check_count":2,"last_check_ok":false,"last_check_at":"2026-11-23T10:00:00Z"}`
	if got != want {
		t.Fatalf("got %s", got)
	}
	if strings.Contains(got, "SECRET") {
		t.Fatal("output in payload")
	}
	if g := string(ProgressPayload(TicketProgress{ItemID: "r/t", State: "started"})); !strings.Contains(g, `"last_check_at":null`) || !strings.Contains(g, `"last_check_ok":null`) {
		t.Fatalf("unset must be null: %s", g)
	}
}
