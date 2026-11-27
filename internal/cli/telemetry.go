package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/kibanyadev/dojo/internal/state"
)

// telemetryNotice is printed once, on stderr, by the first command that could send usage stats.
// Nothing is recorded or sent by that command or before it.
const telemetryNotice = `Dojo shares usage stats: the commands you run, whether they worked and how long
they took. When you are logged in they are tied to your account. Never your files,
paths or what you type. Kept in the EU.
Turn it off any time with: dojo telemetry off
`

const telemetryWhat = `Each event is the command name (like "check"), its result (ok, failed, error or
canceled) and how long it took, sent with this build's version and your OS. Never
arguments, file names, paths or output. Events are kept 12 months. When you are logged
in they are tied to your account; otherwise they are not tied to anyone.
`

const (
	telemetryTimeout = 2 * time.Second // the whole flush at the end of a command
	telemetryBatch   = 20
	telemetryBackoff = time.Minute // after a network error or a 5xx
	maxBackoff       = time.Hour
	telemetryPatchTO = 10 * time.Second
)

func (a *App) getenv(k string) string {
	if a.Getenv != nil {
		return a.Getenv(k)
	}
	return os.Getenv(k)
}

func (a *App) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// telemetryEnvOff reports an environment override that turns usage stats off, and its name.
// The environment can only turn them off, never on: it always wins over the local switch.
func (a *App) telemetryEnvOff() (bool, string) {
	switch strings.ToLower(strings.TrimSpace(a.getenv("DOJO_TELEMETRY"))) {
	case "off", "0", "false", "no", "disabled":
		return true, "DOJO_TELEMETRY"
	}
	switch strings.ToLower(strings.TrimSpace(a.getenv("DO_NOT_TRACK"))) {
	case "1", "true":
		return true, "DO_NOT_TRACK"
	}
	return false, ""
}

// telemetryAPI is where events go; empty when no API is configured, which keeps usage stats silent.
func (a *App) telemetryAPI() string {
	api, err := resolveURL("API URL", "", a.Login.APIURL, DefaultAPIURL, "DOJO_API_URL", false)
	if err != nil {
		return ""
	}
	return api
}

// telemetryActive is true when events may be recorded and sent right now: an API is configured,
// no environment override, the local switch is on, and the notice has been shown on a prior command.
// An unreadable switch file counts as off.
func (a *App) telemetryActive() bool {
	if a.telemetryAPI() == "" {
		return false
	}
	if off, _ := a.telemetryEnvOff(); off {
		return false
	}
	st, err := state.LoadTelemetry()
	return err == nil && !st.Off && st.NoticeShown
}

// telemetryBegin runs before a command. It prints the notice the first time and says whether this
// command is recorded; the command that prints the notice is not.
func (a *App) telemetryBegin(name string) bool {
	if name == "telemetry" || name == "mcp" || a.telemetryAPI() == "" {
		return false
	}
	if off, _ := a.telemetryEnvOff(); off {
		return false
	}
	st, err := state.LoadTelemetry()
	if err != nil || st.Off {
		return false
	}
	if !st.NoticeShown {
		if a.Err == nil {
			return false
		}
		fmt.Fprint(a.Err, telemetryNotice)
		st.NoticeShown = true
		_ = state.SaveTelemetry(st)
		return false
	}
	return true
}

// telemetryEnd queues the finished command and tries to send. Nothing here fails the command.
func (a *App) telemetryEnd(ctx context.Context, name string, took time.Duration, runErr error) {
	if !a.telemetryActive() { // re-checked: the command itself (login) may have switched it off
		return
	}
	ms := min(max(int(took/time.Millisecond), 0), 86_400_000)
	q, err := state.LoadQueue()
	if err != nil {
		q = state.Queue{}
	}
	q.Events = append(q.Events, state.Event{Command: name, Result: resultName(ctx, runErr), DurationMS: ms})
	if ctx.Err() != nil { // interrupted: keep the event, do not hold the exit
		_ = state.SaveQueue(q)
		return
	}
	a.flushTelemetry(ctx, q)
}

func resultName(ctx context.Context, err error) string {
	switch {
	case err == nil:
		return "ok"
	case ctx.Err() != nil || errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, ErrCheckFailed), errors.Is(err, ErrNotReady):
		return "failed"
	default:
		return "error"
	}
}

// flushTelemetry sends the queue in batches of at most 20 within a short deadline and saves what is
// left. A 401 on a bearer drops the batch and holds sending back, never resending it anonymously (the
// account may have usage stats off) and never touching the stored login; a 429 or any other failure
// keeps the events and holds sending back.
func (a *App) flushTelemetry(ctx context.Context, q state.Queue) {
	defer func() { _ = state.SaveQueue(q) }()
	now := a.now()
	if now.Before(q.RetryAfter) {
		return
	}
	q.RetryAfter = time.Time{}
	api := a.telemetryAPI()
	token, _ := state.LoadToken()
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), telemetryTimeout)
	defer cancel()
	for len(q.Events) > 0 {
		n := min(telemetryBatch, len(q.Events))
		body := map[string]any{"cli_version": cleanVersion(Version), "os": runtime.GOOS + "/" + runtime.GOARCH,
			"events": q.Events[:n]}
		r, err := a.callAPI(fctx, http.MethodPost, api, "/v1/telemetry", token, body)
		switch {
		case err != nil:
			q.RetryAfter = now.Add(telemetryBackoff)
			return
		case r.status == http.StatusNoContent, r.status == http.StatusBadRequest: // sent, or never acceptable
			q.Events = q.Events[n:]
		case r.status == http.StatusUnauthorized && token != "": // a bad bearer: never anonymous, login untouched
			q.Events = q.Events[n:]
			q.RetryAfter = now.Add(telemetryBackoff)
			return
		case r.status == http.StatusTooManyRequests:
			q.RetryAfter = now.Add(min(max(r.retryAfter, telemetryBackoff), maxBackoff))
			return
		default:
			q.RetryAfter = now.Add(telemetryBackoff)
			return
		}
	}
}

// telemetryCmd is dojo telemetry [on|off|status].
func (a *App) telemetryCmd(ctx context.Context, args []string) error {
	sub := "status"
	switch {
	case len(args) > 1:
		return errors.New("usage: dojo telemetry [on|off|status]")
	case len(args) == 1:
		sub = args[0]
	}
	if sub != "on" && sub != "off" && sub != "status" {
		return errors.New("usage: dojo telemetry [on|off|status]")
	}
	st, err := state.LoadTelemetry()
	if err != nil {
		st = state.Telemetry{} // an unreadable file is replaced by an explicit choice
		if sub == "status" {
			fmt.Fprintln(a.Out, "Usage stats: off (the settings file is unreadable, so nothing is sent; dojo telemetry on or off rewrites it)")
			return nil
		}
	}
	switch sub {
	case "off":
		st.Off, st.NoticeShown = true, true
		if err := state.SaveTelemetry(st); err != nil {
			return fmt.Errorf("could not save the setting: %w (fix DOJO_HOME)", err)
		}
		if err := state.RemoveQueue(); err != nil {
			return fmt.Errorf("usage stats are off, but the queue file could not be removed: %w", err)
		}
		fmt.Fprintln(a.Out, "Usage stats are off on this computer. Nothing is sent and nothing is queued.")
		a.mirrorUsageStats(ctx, false)
	case "on":
		st.Off, st.NoticeShown = false, true
		if err := state.SaveTelemetry(st); err != nil {
			return fmt.Errorf("could not save the setting: %w (fix DOJO_HOME)", err)
		}
		fmt.Fprintln(a.Out, "Usage stats are on.")
		fmt.Fprint(a.Out, telemetryWhat)
		if off, name := a.telemetryEnvOff(); off {
			fmt.Fprintf(a.Out, "Note: %s is set in your environment and still keeps them off.\n", name)
		}
		if a.telemetryAPI() == "" {
			fmt.Fprintln(a.Out, "No Dojo API is configured (DOJO_API_URL), so nothing is sent yet.")
		}
		a.mirrorUsageStats(ctx, true)
	default:
		if !st.NoticeShown {
			st.NoticeShown = true
			_ = state.SaveTelemetry(st)
		}
		switch off, name := a.telemetryEnvOff(); {
		case off:
			fmt.Fprintf(a.Out, "Usage stats: off (%s is set in your environment)\n", name)
		case st.Off:
			fmt.Fprintln(a.Out, "Usage stats: off (dojo telemetry on turns them on)")
		case a.telemetryAPI() == "":
			fmt.Fprintln(a.Out, "Usage stats: on, but no Dojo API is configured (DOJO_API_URL), so nothing is sent")
		default:
			fmt.Fprintln(a.Out, "Usage stats: on (dojo telemetry off turns them off)")
		}
		fmt.Fprint(a.Out, telemetryWhat)
	}
	return nil
}

// mirrorUsageStats copies the switch to the account when logged in. It never fails the command:
// the local switch is already set, and a failure here is said, with what to do.
func (a *App) mirrorUsageStats(ctx context.Context, on bool) {
	tok, err := state.LoadToken()
	if err != nil {
		return // not logged in (or no readable token): the account setting is not reachable
	}
	word := "off"
	if on {
		word = "on"
	}
	fail := func(why string) {
		fmt.Fprintf(a.Out, "Could not set usage stats %s on your account (%s). This computer keeps its setting; change the account's in Settings on the website.\n", word, why)
	}
	api := a.telemetryAPI()
	if api == "" {
		fail("no Dojo API is configured: set DOJO_API_URL")
		return
	}
	pctx, cancel := context.WithTimeout(ctx, telemetryPatchTO)
	defer cancel()
	r, err := a.callAPI(pctx, http.MethodPatch, api, "/v1/me", tok, map[string]bool{"usage_stats": on})
	switch {
	case err != nil:
		fail(netErr("could not reach the Dojo API", err).Error())
	case r.status == http.StatusOK:
		fmt.Fprintf(a.Out, "Your account setting is %s too.\n", word)
	case r.status == http.StatusUnauthorized:
		fail("your login is no longer valid: run dojo login")
	default:
		fail(fmt.Sprintf("the API answered %d", r.status))
	}
}

// alignUsageStats reads usage_stats from a /v1/me answer at login: an account that turned usage stats
// off turns them off here too, and drops anything queued. An account that has them on changes nothing.
func (a *App) alignUsageStats(r *apiReply) {
	if r == nil || r.status != http.StatusOK {
		return
	}
	var me struct {
		UsageStats *bool `json:"usage_stats"`
	}
	if json.Unmarshal(r.body, &me) != nil || me.UsageStats == nil || *me.UsageStats {
		return
	}
	st, _ := state.LoadTelemetry()
	if !st.Off {
		st.Off, st.NoticeShown = true, true
		if err := state.SaveTelemetry(st); err != nil {
			fmt.Fprintf(a.Out, "Warning: usage stats are off on your account but the local setting could not be saved: %v\n", err)
			return
		}
		fmt.Fprintln(a.Out, "Usage stats are off on your account, so they are off on this computer too.")
	}
	_ = state.RemoveQueue()
}
