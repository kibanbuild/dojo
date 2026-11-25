//go:build integration

package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kibanbuild/dojo/internal/cli"
	"github.com/kibanbuild/dojo/internal/doctor"
	"github.com/kibanbuild/dojo/internal/lab"
	"github.com/kibanbuild/dojo/internal/state"
)

func evDoctor(id, at string, checks ...map[string]string) map[string]any {
	return map[string]any{"id": id, "type": "doctor.run", "at": at, "checks": checks}
}

func dc(id, status, text string) map[string]string {
	return map[string]string{"id": id, "status": status, "text": text, "fix": "fix " + id}
}

const doctorRowsSQL = "SELECT check_id, status, text, source FROM doctor_results r JOIN learners l ON l.id = r.learner_id WHERE l.github_id = 1001 ORDER BY check_id"

func learnerUUID(t *testing.T) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := id.Scan(asOwner(t, "SELECT id FROM learners WHERE github_id = 1001")); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestDoctorRunProjectsLatestPerCheck(t *testing.T) {
	c, tok := eventsFixture(t)
	code, body := post(t, c, tok, evDoctor(evID(), "2026-11-24T09:00:00Z", dc("memory", "ok", "16 GB RAM"), dc("ruleset", "fail", "main has no ruleset")))
	if code != 200 || strings.TrimSpace(body) != `{"accepted":1,"duplicates":0,"ignored":0}` {
		t.Fatalf("first run: %d %q", code, body)
	}
	if got := asOwner(t, doctorRowsSQL); got != "memory|ok|16 GB RAM|cli\nruleset|fail|main has no ruleset|cli" {
		t.Fatalf("rows: %q", got)
	}
	// The events row keeps ids and statuses, never text or fix.
	if got := asOwner(t, "SELECT payload::text FROM events WHERE type = 'doctor.run'"); got != `{"checks": [{"id": "memory", "status": "ok"}, {"id": "ruleset", "status": "fail"}]}` {
		t.Fatalf("payload: %q", got)
	}
	if n := asOwner(t, "SELECT count(*) FROM events WHERE payload::text LIKE '%fix memory%' OR payload::text LIKE '%16 GB%'"); n != "0" {
		t.Fatalf("text or fix leaked into events: %q", n)
	}

	// A later run replaces only the ids it lists; the other row stays. updated_at moves.
	before := asOwner(t, "SELECT updated_at FROM doctor_results WHERE check_id = 'memory'")
	post(t, c, tok, evDoctor(evID(), "2026-11-24T09:05:00Z", dc("memory", "warn", "8 GB RAM")))
	if got := asOwner(t, doctorRowsSQL); got != "memory|warn|8 GB RAM|cli\nruleset|fail|main has no ruleset|cli" {
		t.Fatalf("after second run: %q", got)
	}
	if after := asOwner(t, "SELECT updated_at FROM doctor_results WHERE check_id = 'memory'"); after == before {
		t.Fatalf("updated_at did not move: %q", after)
	}

	// A replayed older run (offline queue) changes nothing; one at the same time wins.
	post(t, c, tok, evDoctor(evID(), "2026-11-24T09:01:00Z", dc("memory", "fail", "old")))
	if got := asOwner(t, "SELECT status FROM doctor_results WHERE check_id = 'memory'"); got != "warn" {
		t.Fatalf("an older run overwrote a newer one: %q", got)
	}
	post(t, c, tok, evDoctor(evID(), "2026-11-24T09:05:00Z", dc("memory", "ok", "same time")))
	if got := asOwner(t, "SELECT text FROM doctor_results WHERE check_id = 'memory'"); got != "same time" {
		t.Fatalf("an equal client_at must win: %q", got)
	}

	// A duplicate id projects nothing, and a client clock ahead of the server counts as the server's.
	dup := evDoctor(evID(), "2026-11-24T10:00:00Z", dc("disk", "ok", "50 GB free disk"))
	post(t, c, tok, dup)
	post(t, c, tok, dup)
	if n := asOwner(t, "SELECT count(*) FROM events WHERE type = 'doctor.run'"); n != "5" {
		t.Fatalf("events stored: %q", n)
	}
	post(t, c, tok, evDoctor(evID(), "2999-01-01T00:00:00Z", dc("cpu", "ok", "8 CPU cores")))
	if got := asOwner(t, "SELECT checked_at <= now() FROM doctor_results WHERE check_id = 'cpu'"); got != "t" {
		t.Fatalf("checked_at not clamped to the server clock: %q", got)
	}
}

func TestDoctorRunIsOnePerLearnerAndMixesWithTicketEvents(t *testing.T) {
	c, tokAda := eventsFixture(t)
	asOwner(t, "DELETE FROM learners WHERE github_id = 1002")
	t.Cleanup(func() { asOwner(t, "DELETE FROM learners WHERE github_id = 1002") })
	signUp(t, "bob")
	tokBob := cliToken(t, c, "bob")
	code, body := post(t, c, tokAda, evStarted(evID(), "ev-role/one", "2026-11-24T08:00:00Z"), evDoctor(evID(), "2026-11-24T09:00:00Z", dc("git", "ok", "git is installed")))
	if code != 200 || strings.TrimSpace(body) != `{"accepted":2,"duplicates":0,"ignored":0}` {
		t.Fatalf("mixed batch: %d %q", code, body)
	}
	post(t, c, tokBob, evDoctor(evID(), "2026-11-24T09:00:00Z", dc("git", "fail", "git is not installed")))
	q := "SELECT r.status, r.org_id = l.org_id FROM doctor_results r JOIN learners l ON l.id = r.learner_id WHERE l.github_id = "
	if got := asOwner(t, q+"1001"); got != "ok|t" {
		t.Fatalf("ada: %q", got)
	}
	if got := asOwner(t, q+"1002"); got != "fail|t" {
		t.Fatalf("bob: %q", got)
	}
}

func TestDoctorRunRefusals(t *testing.T) {
	c, tok := eventsFixture(t)
	ok := dc("git", "ok", "git is installed")
	for name, ev := range map[string]map[string]any{
		"two_factor":   evDoctor(evID(), "2026-11-24T09:00:00Z", dc("two_factor", "ok", "x")),
		"unknown id":   evDoctor(evID(), "2026-11-24T09:00:00Z", dc("nope", "ok", "x")),
		"duplicate id": evDoctor(evID(), "2026-11-24T09:00:00Z", ok, ok),
		"bad status":   evDoctor(evID(), "2026-11-24T09:00:00Z", dc("git", "great", "x")),
		"no checks":    evDoctor(evID(), "2026-11-24T09:00:00Z"),
		"with item_id": {"id": evID(), "type": "doctor.run", "at": "2026-11-24T09:00:00Z", "item_id": "ev-role/one", "checks": []map[string]string{ok}},
	} {
		if code, body := post(t, c, tok, ev); code != 400 || !strings.Contains(body, `"index":0`) {
			t.Fatalf("%s: %d %q", name, code, body)
		}
	}
	if n := asOwner(t, "SELECT count(*) FROM events WHERE type = 'doctor.run'"); n != "0" {
		t.Fatalf("a refused event was stored: %q", n)
	}
	// Text and fix are bounded by the table too.
	out, err := ownerExec(t, "INSERT INTO doctor_results (org_id, learner_id, check_id, status, text, source, checked_at) SELECT org_id, id, 'git', 'ok', '', 'cli', now() FROM learners WHERE github_id = 1001")
	if err == nil || !strings.Contains(out, "violates check constraint") {
		t.Fatalf("empty text: %v %q", err, out)
	}
	out, err = ownerExec(t, "INSERT INTO events (org_id, learner_id, client_event_id, type, client_at) SELECT org_id, id, gen_random_uuid(), 'doctor.nope', now() FROM learners WHERE github_id = 1001")
	if err == nil || !strings.Contains(out, "events_type_check") {
		t.Fatalf("events.type check: %v %q", err, out)
	}
}

func TestDoctorStateReadiness(t *testing.T) {
	c, tok := eventsFixture(t)
	st := expiryStore(t)
	ctx := context.Background()
	learner := learnerUUID(t)
	if checks, ready, err := st.DoctorState(ctx, learner); err != nil || len(checks) != 0 || ready.O3 || ready.O4 {
		t.Fatalf("no rows: %v %+v %+v", err, checks, ready)
	}
	var machine []map[string]string
	for _, id := range []string{"memory", "disk", "cpu", "virtualization", "vm_manager", "git"} {
		machine = append(machine, dc(id, "ok", id+" ok"))
	}
	post(t, c, tok, evDoctor(evID(), "2026-11-24T09:00:00Z", machine...))
	checks, ready, err := st.DoctorState(ctx, learner)
	if err != nil || len(checks) != 6 || !ready.O3 || ready.O4 {
		t.Fatalf("machine only: %v %+v %+v", err, checks, ready)
	}
	if checks[0].ID != "cpu" || checks[0].Group != "machine" || checks[0].Source != "cli" || checks[0].Text != "cpu ok" || checks[0].Fix != "fix cpu" || checks[0].CheckedAt.IsZero() {
		t.Fatalf("first check: %+v", checks[0])
	}
	var key []map[string]string
	for _, id := range []string{"ssh_key", "git_signing", "key_signing", "key_auth", "repo", "ruleset", "agents_md"} {
		key = append(key, dc(id, "ok", id+" ok"))
	}
	key[5] = dc("ruleset", "fail", "main has no ruleset")
	post(t, c, tok, evDoctor(evID(), "2026-11-24T09:01:00Z", key...))
	if _, ready, _ = st.DoctorState(ctx, learner); !ready.O3 || ready.O4 {
		t.Fatalf("a failing ruleset blocks O4: %+v", ready)
	}
	post(t, c, tok, evDoctor(evID(), "2026-11-24T09:02:00Z", dc("ruleset", "unknown", "GitHub unreachable"), dc("key_auth", "warn", "maybe")))
	if _, ready, _ = st.DoctorState(ctx, learner); !ready.O3 || !ready.O4 {
		t.Fatalf("unknown and warn do not block: %+v", ready)
	}
	post(t, c, tok, evDoctor(evID(), "2026-11-24T09:03:00Z", dc("memory", "fail", "4 GB RAM")))
	if _, ready, _ = st.DoctorState(ctx, learner); ready.O3 || !ready.O4 {
		t.Fatalf("a failing machine check blocks O3: %+v", ready)
	}
}

func TestMeCarriesGithubID(t *testing.T) {
	c, tok := eventsFixture(t)
	code, body := c.call(http.MethodGet, "/v1/me", tok, nil)
	var me struct {
		GithubID int64 `json:"github_id"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &me) != nil || me.GithubID != 1001 {
		t.Fatalf("me: %d %q", code, body)
	}
}

func TestDoctorSSEEventAndNoNotifyForAStaleRun(t *testing.T) {
	c, tok, ada := streamFixture(t)
	s := openStream(t, apiURL(t), cookieOf(t, ada))
	s.next("ready", 5*time.Second)
	post(t, c, tok, evDoctor(evID(), "2026-11-24T09:00:00Z", dc("memory", "ok", "SECRET-TEXT"), dc("disk", "warn", "20 GB")))
	ev := s.next("doctor", 5*time.Second)
	if ev.data != `{"checks":[{"id":"disk","status":"warn"},{"id":"memory","status":"ok"}]}` {
		t.Fatalf("doctor event: %s", ev.data)
	}
	// An older run changes nothing, so nothing is sent for it.
	post(t, c, tok, evDoctor(evID(), "2026-11-24T08:00:00Z", dc("memory", "fail", "old")))
	post(t, c, tok, evStarted(evID(), "ev-role/one", "2026-11-24T08:00:00Z"))
	if next := s.next("progress", 5*time.Second); strings.Contains(next.data, "memory") {
		t.Fatalf("progress: %s", next.data)
	}
}

// stubBackend is a VM manager that is always ready; the doctor never calls anything else of it.
type stubBackend struct{ lab.Backend }

func (stubBackend) Name() string     { return "stub" }
func (stubBackend) Available() error { return nil }

// runDoctor runs `dojo doctor` on a good, fake machine whose VM manager is ready.
func runDoctor(t *testing.T) (string, error) {
	t.Helper()
	return runCLIWith(t, "", func(a *cli.App) {
		a.BackendName = "stub"
		a.NewBackend = func(string) (lab.Backend, error) { return stubBackend{}, nil }
		a.Probe = func(context.Context) doctor.Machine {
			return doctor.Machine{OS: "linux", MemoryBytes: 16 << 30, FreeDiskBytes: 100 << 30, CPUs: 8, Virtualization: true,
				VirtualizationKnown: true, HasGit: true, GitName: "Ada", GitEmail: "ada@example.test", HasVSCode: true}
		}
	}, "doctor")
}

func TestDoctorCLIToEventToRowToSSE(t *testing.T) {
	_, _, ada := streamFixture(t)
	t.Setenv("DOJO_HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("DOJO_SSH_DIR", filepath.Join(t.TempDir(), "ssh"))
	if out, err := runCLI(t, "ada", "login"); err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}
	s := openStream(t, apiURL(t), cookieOf(t, ada))
	s.next("ready", 5*time.Second)

	out, err := runDoctor(t)
	if err != nil && !errors.Is(err, cli.ErrNotReady) {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	ev := s.next("doctor", 10*time.Second)
	var got struct {
		Checks []struct{ ID, Status string } `json:"checks"`
	}
	if json.Unmarshal([]byte(ev.data), &got) != nil || len(got.Checks) < 10 || strings.Contains(ev.data, "text") {
		t.Fatalf("doctor event: %s", ev.data)
	}
	if q, _ := state.LoadEvents(); len(q.Events) != 0 {
		t.Fatalf("the run stayed queued: %+v", q.Events)
	}
	if got := asOwner(t, "SELECT status, text, source FROM doctor_results r JOIN learners l ON l.id = r.learner_id WHERE l.github_id = 1001 AND check_id = 'memory'"); got != "ok|16 GB RAM|cli" {
		t.Fatalf("memory row: %q", got)
	}
	_, ready, err := expiryStore(t).DoctorState(context.Background(), learnerUUID(t))
	if err != nil || !ready.O3 || ready.O4 {
		t.Fatalf("readiness after a good machine with no key: %+v %v", ready, err)
	}

	// Logged out the run queues; the next login sends it, and only the latest one waits.
	if out, err := runCLI(t, "", "logout"); err != nil {
		t.Fatalf("logout: %v\n%s", err, out)
	}
	_, _ = runDoctor(t)
	_, _ = runDoctor(t)
	q, _ := state.LoadEvents()
	if len(q.Events) != 1 || q.Events[0].Type != "doctor.run" {
		t.Fatalf("queued events = %+v", q.Events)
	}
}
