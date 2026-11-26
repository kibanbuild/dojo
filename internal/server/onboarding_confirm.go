package server

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kibanbuild/dojo/internal/server/repo"
)

const confirmRoute = "/v1/onboarding/confirmations/{step}"

// StepConfirmer writes the learner's own confirmation of an advisory step (design 3.9).
type StepConfirmer interface {
	ConfirmStep(ctx context.Context, learner pgtype.UUID, id string) error
	UnconfirmStep(ctx context.Context, learner pgtype.UUID, id string) error
}

// confirmStep serves PUT and DELETE /v1/onboarding/confirmations/{step}: two_factor and secret_scanning only.
func (a Auth) confirmStep(confirm bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !choiceGuard(w, r) {
			return
		}
		step := r.PathValue("step")
		if !repo.SelfConfirmable(step) {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		do := a.Onboarding.Confirmer.UnconfirmStep
		if confirm {
			do = a.Onboarding.Confirmer.ConfirmStep
		}
		if err := do(context.WithoutCancel(r.Context()), principalLearner(r), step); err != nil {
			choiceFailed(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
