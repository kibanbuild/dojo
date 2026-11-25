package repo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// doctorGroups maps each check id the CLI may report to its group (design 3.9). two_factor and
// secret_scanning are absent on purpose: only the learner confirms them.
var doctorGroups = map[string]string{
	"memory": "machine", "disk": "machine", "cpu": "machine", "virtualization": "machine", "vm_manager": "machine",
	"git": "machine", "git_identity": "machine", "editor": "machine",
	"git_email_github": "key", "ssh_key": "key", "ssh_keygen": "key", "git_signing": "key", "key_signing": "key", "key_auth": "key",
	"repo": "repo", "ruleset": "repo", "agents_md": "repo",
}

// checkGroup is the group of a stored check: the CLI ids above, plus the two the learner confirms
// (advisory, never gating).
func checkGroup(id string) string {
	if g, ok := doctorGroups[id]; ok {
		return g
	}
	if id == "two_factor" || id == "secret_scanning" {
		return "advisory"
	}
	return ""
}

// ValidDoctorCheckID reports whether a doctor.run event may carry the id.
func ValidDoctorCheckID(id string) bool { _, ok := doctorGroups[id]; return ok }

// The ids each onboarding step requires (design 3.9). Advisory ids never gate.
var (
	o3Required = []string{"memory", "disk", "cpu", "virtualization", "vm_manager", "git"}
	o4Required = []string{"ssh_key", "git_signing", "key_signing", "key_auth", "repo", "ruleset", "agents_md"}
)

// DoctorCheck is the latest stored result of one check.
type DoctorCheck struct {
	ID        string
	Group     string
	Status    string
	Text, Fix string
	Source    string
	CheckedAt time.Time
}

// Readiness says whether the O3 (machine) and O4 (key and repo) steps are ready.
type Readiness struct{ O3, O4 bool }

// StepReadiness applies the readiness rule: a step is ready when every id it requires has a row and
// none of them is fail. warn and unknown do not block, and no rows means not ready.
func StepReadiness(checks []DoctorCheck) Readiness {
	status := make(map[string]string, len(checks))
	for _, c := range checks {
		status[c.ID] = c.Status
	}
	ready := func(ids []string) bool {
		for _, id := range ids {
			if s, ok := status[id]; !ok || s == "fail" {
				return false
			}
		}
		return true
	}
	return Readiness{O3: ready(o3Required), O4: ready(o4Required)}
}

// DoctorState returns the learner's latest result per check, in check id order, and the readiness
// of the O3 and O4 steps. GET /v1/onboarding reads it (design 3.9).
func (s *Store) DoctorState(ctx context.Context, learner pgtype.UUID) ([]DoctorCheck, Readiness, error) {
	rows, err := s.q.ListDoctorResults(ctx, learner)
	if err != nil {
		return nil, Readiness{}, fmt.Errorf("list doctor results: %w", err)
	}
	out := make([]DoctorCheck, 0, len(rows))
	for _, r := range rows {
		out = append(out, DoctorCheck{ID: r.CheckID, Group: checkGroup(r.CheckID), Status: r.Status,
			Text: r.Text, Fix: r.Fix, Source: r.Source, CheckedAt: r.CheckedAt.Time})
	}
	return out, StepReadiness(out), nil
}

// DoctorCheckIn is one check of a doctor.run event.
type DoctorCheckIn struct{ ID, Status, Text, Fix string }

// DoctorChangePayload is the NOTIFY message of a doctor.run projection: the ids it changed, with no
// text or fix. The stream sends it as `event: doctor` (the event key picks the name).
func DoctorChangePayload(changed []DoctorChange) []byte {
	type check struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	m := struct {
		Event  string  `json:"event"`
		Checks []check `json:"checks"`
	}{Event: "doctor", Checks: make([]check, 0, len(changed))}
	for _, c := range changed {
		m.Checks = append(m.Checks, check(c))
	}
	b, _ := json.Marshal(m) // plain fields: cannot fail
	return b
}

// DoctorChange is one check a projection changed.
type DoctorChange struct{ ID, Status string }

// projectDoctorRun upserts the checks of one doctor.run event and records the ones that changed in
// changed (a later event of the same batch wins). An older run than the stored row changes nothing.
func projectDoctorRun(ctx context.Context, q *Queries, orgID, learnerID pgtype.UUID, e EventIn, now time.Time, changed map[string]string) error {
	at := e.ClientAt
	if at.After(now) {
		at = now
	}
	for _, c := range e.Checks {
		row, err := q.UpsertDoctorResult(ctx, UpsertDoctorResultParams{
			OrgID: orgID, LearnerID: learnerID, CheckID: c.ID, Status: c.Status, Text: c.Text, Fix: c.Fix, CheckedAt: ts(at),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			continue // the stored result is newer
		}
		if err != nil {
			return fmt.Errorf("upsert doctor result: %w", err)
		}
		changed[row.CheckID] = row.Status
	}
	return nil
}

// notifyDoctor sends one message with every check the transaction changed, at commit.
func notifyDoctor(ctx context.Context, tx pgx.Tx, learner pgtype.UUID, changed map[string]string) error {
	if len(changed) == 0 {
		return nil
	}
	ids := make([]string, 0, len(changed))
	for id := range changed {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	list := make([]DoctorChange, 0, len(ids))
	for _, id := range ids {
		list = append(list, DoctorChange{id, changed[id]})
	}
	if err := notifyLearner(ctx, tx, learner, DoctorChangePayload(list)); err != nil {
		return fmt.Errorf("notify doctor: %w", err)
	}
	return nil
}
