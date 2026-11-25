package server

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kibanbuild/dojo/internal/mentor"
	"github.com/kibanbuild/dojo/internal/server/repo"
)

const onboardingPath = "/v1/onboarding"

// OnboardingFacts reads the two flags next_step needs beyond the catalog and the doctor rows.
type OnboardingFacts interface {
	GetOnboardingFacts(ctx context.Context, id pgtype.UUID) (repo.GetOnboardingFactsRow, error)
}

// DoctorReader reads the latest doctor result per check and the readiness of O3 and O4.
type DoctorReader interface {
	DoctorState(ctx context.Context, learner pgtype.UUID) ([]repo.DoctorCheck, repo.Readiness, error)
}

// Onboarding backs GET /v1/onboarding (design 3.9).
type Onboarding struct {
	Facts        OnboardingFacts
	Doctor       DoctorReader
	Env          string // DOJO_ENV
	PublicOrigin string // PUBLIC_ORIGIN
}

type onboardingInputs struct {
	finished, chosen, cliConnected bool
	ready                          repo.Readiness
}

// nextStep derives the step on every read, never stored; done is sticky (design 3.9).
func nextStep(in onboardingInputs) string {
	switch {
	case in.finished:
		return "done"
	case !in.chosen:
		return "o0"
	case !in.cliConnected:
		return "o2"
	case !in.ready.O3:
		return "o3"
	case !in.ready.O4:
		return "o4"
	}
	return "o5"
}

type onboardingPathOut struct {
	Slug   *string `json:"slug"`
	Chosen bool    `json:"chosen"`
}

type onboardingCheck struct {
	ID        string `json:"id"`
	Group     string `json:"group"`
	Status    string `json:"status"`
	Text      string `json:"text"`
	Fix       string `json:"fix"`
	Source    string `json:"source"`
	CheckedAt string `json:"checked_at"`
}

type onboardingResponse struct {
	NextStep     string            `json:"next_step"`
	Env          string            `json:"env"`
	PublicOrigin string            `json:"public_origin"`
	Path         onboardingPathOut `json:"path"`
	CLIConnected bool              `json:"cli_connected"`
	Checks       []onboardingCheck `json:"checks"`
	AgentsMD     string            `json:"agents_md"`
	FirstTicket  *firstTicket      `json:"first_ticket"`
}

type firstTicket struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// onboarding is GET /v1/onboarding: one read for O2 to O5 and for routing a signed-in learner.
func (a Auth) onboarding(w http.ResponseWriter, r *http.Request) {
	p, _ := principalOf(r.Context())
	o := a.Onboarding
	path, seen, err := a.loadPath(r)
	if err != nil {
		readFailed(w, r, err)
		return
	}
	facts, err := o.Facts.GetOnboardingFacts(r.Context(), p.learner)
	if err != nil {
		readFailed(w, r, err)
		return
	}
	checks, ready, err := o.Doctor.DoctorState(r.Context(), p.learner)
	if err != nil {
		readFailed(w, r, err)
		return
	}
	out := onboardingResponse{
		NextStep: nextStep(onboardingInputs{finished: facts.Finished, chosen: path.Chosen, cliConnected: facts.CliConnected, ready: ready}),
		Env:      o.Env, PublicOrigin: o.PublicOrigin, Path: onboardingPathOut{path.Path, path.Chosen},
		CLIConnected: facts.CliConnected, Checks: make([]onboardingCheck, 0, len(checks)), AgentsMD: mentor.AgentsMD(),
	}
	for _, c := range checks {
		out.Checks = append(out.Checks, onboardingCheck{c.ID, c.Group, c.Status, c.Text, c.Fix, c.Source, c.CheckedAt.UTC().Format(time.RFC3339)})
	}
	if next := selectToday(path.Roles, seen).Next; next != nil {
		out.FirstTicket = &firstTicket{next.ID, next.Title}
	}
	writeNoStore(w, out)
}
