package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/kibanbuild/dojo/internal/doctor"
	"github.com/kibanbuild/dojo/internal/ghread"
	"github.com/kibanbuild/dojo/internal/state"
)

const meTimeout = 5 * time.Second

// githubChecks runs the doctor checks that read GitHub. The login comes from the Dojo API; logged
// out, nothing is checked or sent for those ids. It returns the results and a line to print when
// it skipped them.
func (a *App) githubChecks(ctx context.Context, m doctor.Machine) ([]doctor.Result, string) {
	const loginLine = "log in to check your GitHub steps (dojo login)"
	token, err := state.LoadToken()
	api := a.telemetryAPI()
	if err != nil || api == "" {
		return nil, loginLine
	}
	mctx, cancel := context.WithTimeout(ctx, meTimeout)
	defer cancel()
	r, err := a.callAPI(mctx, http.MethodGet, api, "/v1/me", token, nil)
	if err != nil {
		return nil, "GitHub steps not checked: could not reach Dojo to read your GitHub login"
	}
	var me struct {
		Login string `json:"github_login"`
		ID    int64  `json:"github_id"`
	}
	switch {
	case r.status == http.StatusUnauthorized:
		return nil, loginLine
	case r.status != http.StatusOK || json.Unmarshal(r.body, &me) != nil || me.Login == "" || me.ID <= 0:
		return nil, "GitHub steps not checked: Dojo did not say which GitHub account you use"
	}
	cfg, err := ghread.LoadConfig(a.getenv)
	if err != nil {
		return nil, "GitHub steps not checked: " + err.Error()
	}
	gh, err := ghread.New(cfg)
	if err != nil {
		return nil, "GitHub steps not checked: " + err.Error()
	}
	probe := a.ProbeGitHub
	if probe == nil {
		probe = func(ctx context.Context, m *doctor.Machine) {
			repo := ""
			if st, err := state.Load(); err == nil {
				repo = st.Repo
			}
			doctor.ProbeGitHub(ctx, m, a.getenv("DOJO_GITHUB_GIT_URL"), repo)
		}
	}
	probe(ctx, &m)
	if ctx.Err() != nil {
		return nil, ""
	}
	return doctor.EvaluateGitHub(ctx, m, doctor.Account{Login: me.Login, ID: me.ID}, gh), ""
}

func printSkipped(a *App, line string) {
	if line != "" {
		fmt.Fprintf(a.Out, "  - %s\n", line)
	}
}
