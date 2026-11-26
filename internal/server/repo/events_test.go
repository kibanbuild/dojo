package repo

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

var (
	t0  = time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	now = t0.Add(time.Hour)
)

func ev(typ string, at time.Time) EventIn { return EventIn{Type: typ, ItemID: "r/t", ClientAt: at} }

func run(cur *TicketProgress, e EventIn) TicketProgress {
	return projectEvent(cur, pgtype.UUID{}, pgtype.UUID{}, e, now)
}

func TestStartedCreatesRowAndClampsFutureClock(t *testing.T) {
	e := ev("ticket.started", now.Add(time.Hour))
	e.Lab = "local"
	p := run(nil, e)
	if p.State != "started" || !p.FirstStartedAt.Time.Equal(now) || p.LabState.String != "starting" || p.Lab.String != "local" || !p.LabStateAt.Time.Equal(now) {
		t.Fatalf("%+v", p)
	}
}

func TestOlderLabEventChangesNothing(t *testing.T) {
	p := run(nil, ev("lab.started", t0.Add(10*time.Minute)))
	if p.LabState.String != "running" || p.State != "started" || !p.FirstStartedAt.Time.Equal(t0.Add(10*time.Minute)) {
		t.Fatalf("lab.* without a row: %+v", p)
	}
	q := run(&p, ev("lab.failed", t0.Add(5*time.Minute)))
	if q.LabState.String != "running" || !q.LabStateAt.Time.Equal(t0.Add(10*time.Minute)) {
		t.Fatalf("older event moved the lab: %+v", q)
	}
	q = run(&p, ev("lab.stopped", t0.Add(10*time.Minute)))
	if q.LabState.String != "stopped" {
		t.Fatalf("equal time must apply: %+v", q)
	}
}

func TestCheckOrderingCountsAlwaysAndPassesOnce(t *testing.T) {
	p := run(nil, ev("ticket.started", t0))
	p.CreatedAt = ts(t0)
	pass := ev("check.run", t0.Add(30*time.Minute))
	pass.Result, pass.Output = "pass", ptr("ok")
	p = run(&p, pass)
	if p.State != "passed" || p.CheckCount != 1 || !p.FirstPassedAt.Time.Equal(now) || p.PassedSeconds.Int32 != 1800 || !p.LastCheckOk.Bool {
		t.Fatalf("pass: %+v", p)
	}
	old := ev("check.run", t0.Add(10*time.Minute))
	old.Result, old.Output = "fail", ptr("old")
	q := run(&p, old)
	if q.CheckCount != 2 || q.State != "passed" || !q.LastCheckOk.Bool || q.LastCheckOutput.String != "ok" || !q.FirstPassedAt.Time.Equal(p.FirstPassedAt.Time) {
		t.Fatalf("replayed older check: %+v", q)
	}
}

func TestPassedSecondsClamp(t *testing.T) {
	p := run(nil, ev("ticket.started", t0))
	p.CreatedAt = ts(now.Add(-10 * time.Minute)) // the row has existed 10 minutes only
	pass := ev("check.run", t0.Add(40*time.Minute))
	pass.Result = "pass"
	if q := run(&p, pass); q.PassedSeconds.Int32 != 600 {
		t.Fatalf("clamp to server time: %d", q.PassedSeconds.Int32)
	}
	p.FirstStartedAt = ts(t0.Add(50 * time.Minute))
	if q := run(&p, pass); q.PassedSeconds.Int32 != 0 {
		t.Fatalf("clamp at 0: %d", q.PassedSeconds.Int32)
	}
}

func TestRevisitRules(t *testing.T) {
	p := run(nil, ev("ticket.started", t0))
	pass := ev("check.run", t0.Add(20*time.Minute))
	pass.Result = "pass"
	p = run(&p, pass)

	older := ev("ticket.started", t0.Add(5*time.Minute))
	if q := run(&p, older); q.State != "passed" {
		t.Fatalf("a start recorded before the pass must not revisit: %s", q.State)
	}
	// first_passed_at is the server time of the pass (now); a start whose client time is after it revisits.
	p.FirstPassedAt = ts(t0.Add(20 * time.Minute))
	again := ev("ticket.started", t0.Add(30*time.Minute))
	q := run(&p, again)
	if q.State != "revisit" || !q.FirstPassedAt.Time.Equal(p.FirstPassedAt.Time) || q.PassedSeconds != p.PassedSeconds {
		t.Fatalf("revisit: %+v", q)
	}
	if r := run(&q, again); r.State != "revisit" {
		t.Fatalf("started keeps revisit: %s", r.State)
	}
	late := ev("check.run", t0.Add(25*time.Minute))
	late.Result = "pass"
	if r := run(&q, late); r.State != "passed" || !r.FirstPassedAt.Time.Equal(p.FirstPassedAt.Time) {
		t.Fatalf("pass in revisit: %+v", r)
	}
	fail := ev("check.run", t0.Add(40*time.Minute))
	fail.Result = "fail"
	if r := run(&q, fail); r.State != "revisit" {
		t.Fatalf("fail changed state: %s", r.State)
	}
}

func TestFailNeverChangesStateAndOutputIsReplaced(t *testing.T) {
	p := run(nil, ev("ticket.started", t0))
	f := ev("check.run", t0.Add(time.Minute))
	f.Result, f.Output = "fail", ptr("a")
	p = run(&p, f)
	g := ev("check.run", t0.Add(2*time.Minute))
	g.Result = "fail"
	p = run(&p, g)
	if p.State != "started" || p.LastCheckOutput.Valid || p.CheckCount != 2 {
		t.Fatalf("%+v", p)
	}
}

func ptr(s string) *string { return &s }

func TestAcceptCommitFirstShaWins(t *testing.T) {
	const old, fresh = "1111111111111111111111111111111111111111", "2222222222222222222222222222222222222222"
	row := func(state, commitState, sha string) *TicketProgress {
		p := &TicketProgress{State: state}
		if commitState != "" {
			p.CommitState = pgtype.Text{String: commitState, Valid: true}
		}
		if sha != "" {
			p.PassedCommit = pgtype.Text{String: sha, Valid: true}
		}
		return p
	}
	tests := []struct {
		name string
		cur  *TicketProgress
		want bool
	}{
		{"no row", nil, false},
		{"started ticket", row("started", "", ""), false},
		{"passed, no sha yet", row("passed", "", ""), true},
		{"revisit, no sha yet", row("revisit", "", ""), true},
		{"pending keeps the first sha", row("passed", "pending", old), false},
		{"verified keeps the first sha", row("passed", "verified", old), false},
		{"unverified takes a new sha", row("passed", "unverified", old), true},
		{"unverified ignores the same sha", row("passed", "unverified", fresh), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := acceptCommit(tt.cur, fresh, now)
			if ok != tt.want {
				t.Fatalf("accepted %v, want %v", ok, tt.want)
			}
			if !ok {
				return
			}
			if got.PassedCommit.String != fresh || got.CommitState.String != "pending" || got.CommitTries != 0 ||
				!got.CommitAt.Time.Equal(now) || !got.CommitNextAt.Time.Equal(now.Add(5*time.Second)) {
				t.Fatalf("accepted row: %+v", got)
			}
		})
	}
}

func TestAcceptCommitResetsTries(t *testing.T) {
	cur := &TicketProgress{State: "passed", CommitTries: 6, CommitState: pgtype.Text{String: "unverified", Valid: true},
		PassedCommit: pgtype.Text{String: "1111111111111111111111111111111111111111", Valid: true}, CommitAt: ts(t0)}
	got, ok := acceptCommit(cur, "2222222222222222222222222222222222222222", now)
	if !ok || got.CommitTries != 0 || !got.CommitAt.Time.Equal(now) || cur.CommitTries != 6 {
		t.Fatalf("%v %+v (input %+v)", ok, got, cur)
	}
}
