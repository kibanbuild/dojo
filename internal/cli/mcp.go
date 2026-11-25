package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/kibanbuild/dojo/internal/catalog"
	"github.com/kibanbuild/dojo/internal/mcp"
	"github.com/kibanbuild/dojo/internal/mentor"
	"github.com/kibanbuild/dojo/internal/state"
)

const noTicket = "No ticket is in progress. Ask the learner to run `dojo start <id>`."

// mcp serves the mentor tools over stdin and stdout; logs go to stderr only.
func (a *App) mcp(ctx context.Context) error {
	in := a.In
	if in == nil {
		in = strings.NewReader("")
	}
	return a.mcpServer().Serve(ctx, in, a.Out)
}

// mcpServer's tools read the learner's progress at each call and expose only what the
// learner already sees: never setup.sh, check.sh, solutions or hints not yet opened.
func (a *App) mcpServer() *mcp.Server {
	const role = " You are the learner's mentor: explain and point to documentation, and never give the fix or the command that solves the ticket."
	return &mcp.Server{
		Name: "dojo", Version: "dev", Instructions: mentor.Rules, Log: a.Err,
		Tools: []mcp.Tool{
			{Name: "current_ticket", Run: a.toolTicket,
				Description: "The ticket the learner is working on: id, title and the task statement they see." + role},
			{Name: "last_check", Run: a.toolLastCheck,
				Description: "The output of the learner's last `dojo check`, to see what still fails." + role},
			{Name: "hints_opened", Run: a.toolHints,
				Description: "The written hints the learner has already opened with `dojo hint`. Hints not yet opened are not available; do not ask for them." + role},
		},
	}
}

// mcpCurrent returns the progress and the current ticket, or a nil ticket when none started.
func (a *App) mcpCurrent() (*state.State, *catalog.Ticket, error) {
	st, err := a.load()
	if err != nil || st.Current == "" {
		return st, nil, err
	}
	k, err := a.loadTicket(st.Current)
	return st, k, err
}

func (a *App) toolTicket() (string, error) {
	st, k, err := a.mcpCurrent()
	if k == nil {
		return noTicket, err
	}
	text := fmt.Sprintf("Ticket %s: %s\n\n%s\n", k.ID, k.Title, strings.TrimSpace(k.Brief))
	if t, ok := st.Passed[k.ID]; ok {
		text += "\nThe learner passed this ticket on " + t.Format("2006-01-02") + ".\n"
	}
	return text, nil
}

func (a *App) toolLastCheck() (string, error) {
	st, k, err := a.mcpCurrent()
	switch {
	case k == nil:
		return noTicket, err
	case st.LastCheck == "":
		return "The learner has not run `dojo check` on this ticket yet.", nil
	}
	return "Output of the last `dojo check`:\n\n" + st.LastCheck, nil
}

// hintsFromAPI reads the hints the learner opened on the web (GET /v1/tickets/{id} returns the
// opened ones, or all once the ticket is passed). Logged out or offline it says so.
func (a *App) hintsFromAPI(id string) (string, error) {
	tok, err := state.LoadToken()
	if err != nil {
		return "", errors.New("cannot read the opened hints: the learner is not logged in (ask them to run `dojo login`)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := a.callAPI(ctx, http.MethodGet, a.telemetryAPI(), "/v1/tickets/"+escapeID(id), tok, nil)
	if err != nil {
		return "", fmt.Errorf("cannot read the opened hints: the dojo API is not reachable (%w)", netErr("request", err))
	}
	if r.status != http.StatusOK {
		return "", fmt.Errorf("cannot read the opened hints: the API answered %d", r.status)
	}
	var t struct {
		HintCount int `json:"hint_count"`
		Hints     []struct {
			N    int    `json:"n"`
			Text string `json:"text"`
		} `json:"hints"`
	}
	if err := json.Unmarshal(r.body, &t); err != nil {
		return "", errors.New("cannot read the opened hints: the API answer was not understood")
	}
	if len(t.Hints) == 0 {
		return "The learner has not opened any hint yet.", nil
	}
	var b strings.Builder
	for _, h := range t.Hints {
		fmt.Fprintf(&b, "Hint %d/%d: %s\n", h.N, t.HintCount, h.Text)
	}
	return b.String(), nil
}

func (a *App) toolHints() (string, error) {
	st, k, err := a.mcpCurrent()
	if k == nil {
		return noTicket, err
	}
	if a.bundleMode() {
		return a.hintsFromAPI(k.ID)
	}
	n := min(max(st.HintsShown, 0), len(k.Hints))
	if n == 0 {
		return "The learner has not opened any hint yet.", nil
	}
	var b strings.Builder
	for i, h := range k.Hints[:n] {
		fmt.Fprintf(&b, "Hint %d/%d: %s\n", i+1, len(k.Hints), h)
	}
	return b.String(), nil
}
