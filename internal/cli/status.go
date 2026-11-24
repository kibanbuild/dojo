package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/kibanbuild/dojo/internal/catalog"
	"github.com/kibanbuild/dojo/internal/state"
)

// status reports local facts only: the current ticket, the lab, the login and the event queue.
// It never calls the network, so it works offline and logged out.
func (a *App) status(ctx context.Context) error {
	st, err := a.load()
	if err != nil {
		p, _ := state.Path()
		fmt.Fprintf(a.Out, "Ticket: unreadable (progress file %s is damaged or locked; remove it, DOJO_HOME moves it)\n\n", p)
		st = &state.State{}
	} else if st.Current == "" {
		fmt.Fprintln(a.Out, "No ticket in progress: run dojo start <role>/<ticket>.")
	} else if k, err := catalog.LoadTicket(a.Catalog, st.Current); err == nil {
		a.printBrief(k)
		if t, ok := st.Passed[k.ID]; ok {
			fmt.Fprintf(a.Out, "Passed on %s.\n", t.Format("2006-01-02"))
		}
	} else {
		fmt.Fprintf(a.Out, "%s (not in this catalog)\n\n", st.Current)
	}
	fmt.Fprintln(a.Out, "Lab: "+a.labState(ctx))
	fmt.Fprintln(a.Out, "Login: "+loginState())
	fmt.Fprintln(a.Out, "Events: "+a.eventsState())
	return nil
}

func (a *App) labState(ctx context.Context) string {
	backend, err := a.NewBackend(a.BackendName)
	if err != nil || backend.Available() != nil {
		return "unknown (the lab backend is not available)"
	}
	ok, err := backend.Exists(ctx)
	switch {
	case err != nil:
		return fmt.Sprintf("unknown (%s)", backend.Name())
	case ok:
		return fmt.Sprintf("running (%s)", backend.Name())
	}
	return "none"
}

func loginState() string {
	switch _, err := state.LoadToken(); {
	case err == nil:
		return "signed in"
	case errors.Is(err, state.ErrNoToken):
		return "not signed in (dojo login)"
	}
	return "unknown (the token file cannot be read)"
}

func (a *App) eventsState() string {
	q, err := state.LoadEvents()
	if err != nil {
		return "unreadable (the queue file is damaged or locked)"
	}
	n := len(q.Events)
	if n == 0 {
		return "none waiting"
	}
	s := fmt.Sprintf("%d waiting", n)
	switch {
	case q.Hold:
		s += ", sending held until the next dojo login"
	case a.now().Before(q.RetryAfter):
		s += ", sending held until " + q.RetryAfter.Local().Format("15:04 MST")
	}
	return s
}

// hint points at the ticket page, where the written hints are, until the AI hint ships (M7).
// Without a configured API address there is no page to name, so the local catalog's hints stay.
func (a *App) hint() error {
	api := a.telemetryAPI()
	if api == "" {
		return a.localHint()
	}
	st, err := a.load()
	if err != nil {
		return err
	}
	if st.Current == "" {
		return errors.New("no ticket in progress: run dojo start <role>/<ticket>")
	}
	fmt.Fprintf(a.Out, "The hints for %s are on its ticket page: %s/app/t/%s\n", st.Current, api, escapeID(st.Current))
	return nil
}

// escapeID escapes each segment of a ticket id and keeps the slashes between them.
func escapeID(id string) string {
	parts := strings.Split(id, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}
