package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/kibanbuild/dojo/internal/state"
)

const (
	hostedSoon      = "Hosted labs are coming soon."
	hostedSoonAdvis = " Turn off the hosted default in Settings to use a local lab."
)

// hostedDefaultOf reads hosted_default from a /v1/me answer.
func hostedDefaultOf(r *apiReply) (value, ok bool) {
	if r == nil || r.status != http.StatusOK {
		return false, false
	}
	var me struct {
		HostedDefault *bool `json:"hosted_default"`
	}
	if json.Unmarshal(r.body, &me) != nil || me.HostedDefault == nil {
		return false, false
	}
	return *me.HostedDefault, true
}

// rememberHostedDefault keeps the account's hosted_default in the state file for offline starts.
func (a *App) rememberHostedDefault(r *apiReply) {
	v, ok := hostedDefaultOf(r)
	if !ok {
		return
	}
	st, err := state.Load()
	if err != nil || st.HostedDefault == v {
		return
	}
	st.HostedDefault = v
	_ = st.Save()
}

// hostedGate refuses dojo start while hosted labs do not exist yet (design 3.9): with --hosted, or
// when the account's hosted default is on. Online it reads the flag from /v1/me first; offline it
// uses the last value the state file kept. It runs before any bundle fetch or event.
func (a *App) hostedGate(ctx context.Context, hostedFlag bool) error {
	if a.bundleMode() {
		if token, err := state.LoadToken(); err == nil {
			if r, err := a.callAPI(ctx, http.MethodGet, a.telemetryAPI(), "/v1/me", token, nil); err == nil {
				a.rememberHostedDefault(r)
			}
		}
	}
	st, err := a.load()
	if err != nil {
		return err
	}
	switch {
	case st.HostedDefault:
		fmt.Fprintln(a.Out, hostedSoon+hostedSoonAdvis)
	case hostedFlag:
		fmt.Fprintln(a.Out, hostedSoon)
	default:
		return nil
	}
	return ErrNotReady
}
