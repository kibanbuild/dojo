package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kibanyadev/dojo/internal/doctor"
	"github.com/kibanyadev/dojo/internal/lab"
	"github.com/kibanyadev/dojo/internal/state"
)

const (
	eventsBatch      = 50
	eventsBatchBytes = 900 * 1024
	eventMaxBytes    = 64 * 1024
	eventOutputBytes = 16 * 1024
	eventDetailChars = 512
	eventsBackoff    = time.Minute
)

// eventsEnabled is true when progress events are recorded: an API is configured and the tickets
// are the real catalog, not a DOJO_KATAS development directory. Usage-stats switches do not apply:
// these events are the learner's progress, not telemetry.
func (a *App) eventsEnabled() bool {
	return a.telemetryAPI() != "" && a.getenv("DOJO_KATAS") == ""
}

// newEventID returns a UUIDv7 in the canonical 8-4-4-4-12 form.
func newEventID(t time.Time) string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	ms := uint64(max(t.UnixMilli(), 0)) //nolint:gosec // G115: clamped to non-negative
	for i := range 6 {
		b[i] = byte(ms >> (8 * (5 - i)) & 0xff) //nolint:gosec // G115: masked to one byte
	}
	b[6] = b[6]&0x0f | 0x70
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// recordEvent queues one progress event and never fails the command.
func (a *App) recordEvent(ev state.ProgressEvent) string {
	if !a.eventsEnabled() {
		return ""
	}
	now := a.now()
	ev.ID, ev.At = newEventID(now), now.UTC().Format(time.RFC3339)
	ev.Detail = cutChars(ev.Detail, eventDetailChars)
	ev.Output = fitOutput(&ev)
	q, err := state.LoadEvents()
	if errors.Is(err, state.ErrCorruptEvents) {
		q = state.EventQueue{} // unreadable content is replaced; an I/O or permission error leaves the file alone
	} else if err != nil {
		return ""
	}
	if acct := state.LoadAccount(); acct != "" {
		if q.Account != "" && q.Account != acct {
			q = state.EventQueue{}
		}
		q.Account = acct
	}
	if ev.Type == "doctor.run" {
		q.Events = dropDoctorRuns(q.Events) // an older result is stale
	}
	q.Events = append(q.Events, ev)
	_ = state.SaveEvents(q)
	return ev.ID
}

// dropDoctorRuns removes every unsent doctor.run, so the queue keeps only the latest one.
func dropDoctorRuns(evs []state.ProgressEvent) []state.ProgressEvent {
	kept := make([]state.ProgressEvent, 0, len(evs))
	for _, e := range evs {
		if e.Type != "doctor.run" {
			kept = append(kept, e)
		}
	}
	return kept
}

// recordDoctor queues the report of dojo doctor as one doctor.run event.
func (a *App) recordDoctor(results []doctor.Result) {
	home, _ := os.UserHomeDir()
	var checks []state.DoctorCheck
	for _, c := range doctor.WireChecks(results, home) {
		checks = append(checks, state.DoctorCheck(c))
	}
	a.recordEvent(state.ProgressEvent{Type: "doctor.run", Checks: checks})
}

// cutChars keeps at most n characters, cut on a character boundary.
func cutChars(s string, n int) string {
	s = strings.ToValidUTF8(s, "�")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// tailBytes keeps the last n bytes of s, starting on a character boundary.
func tailBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return s
}

// fitOutput keeps the last 16 KB of the output and cuts further, still keeping the tail, until the
// whole event is at most 64 KB on the wire.
func fitOutput(ev *state.ProgressEvent) string {
	out := tailBytes(strings.ToValidUTF8(ev.Output, "�"), eventOutputBytes)
	for out != "" {
		ev.Output = out
		raw, err := json.Marshal(ev)
		if err != nil || len(raw) <= eventMaxBytes {
			break
		}
		// A byte costs between 1 and 6 on the wire, so cut by the least that surely helps and look again.
		out = tailBytes(out, max(len(out)-max((len(raw)-eventMaxBytes)/6, 1), 0))
	}
	return out
}

// labFailureReason maps a lab error to the events API's reasons; anything unmapped is other.
func labFailureReason(err error) string {
	if r := lab.FailReason(err); r != "" {
		return r
	}
	return "other"
}

// lastLine is the last non-empty line of s, the part of an error that says what went wrong.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// catalogVersion reads the version of the cached bundle of an item from its sidecar in
// DOJO_HOME/bundles, or "" when there is none.
func catalogVersion(item string) string {
	dir, err := state.Dir()
	if err != nil {
		return ""
	}
	files, _ := filepath.Glob(filepath.Join(dir, "bundles", "*.json"))
	for _, f := range files {
		raw, err := os.ReadFile(f) //nolint:gosec // G304: inside the dojo home
		if err != nil {
			continue
		}
		var sc struct {
			ItemID         string `json:"item_id"`
			CatalogVersion string `json:"catalog_version"`
		}
		if json.Unmarshal(raw, &sc) == nil && sc.ItemID == item && sc.CatalogVersion != "" {
			return sc.CatalogVersion
		}
	}
	return ""
}

// suspendedNotice is printed when the API refuses progress for a suspended account; the events
// stay queued and go out once the account is restored.
const suspendedNotice = "This account is suspended: your progress stays on this computer and is sent once it is restored."

// flushEvents sends the queue in batches within a short deadline and saves what is left. It never
// fails the command.
func (a *App) flushEvents(ctx context.Context) {
	if ctx.Err() != nil || !a.eventsEnabled() {
		return
	}
	q, err := state.LoadEvents()
	if err != nil || len(q.Events) == 0 || q.Hold || a.now().Before(q.RetryAfter) {
		return
	}
	token, err := state.LoadToken()
	if err != nil {
		return // logged out: the events wait for the next login
	}
	now := a.now()
	done := map[string]bool{}
	var hold bool
	var retry time.Time
	defer func() { settleEvents(done, hold, retry) }()
	api := a.telemetryAPI()
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), telemetryTimeout)
	defer cancel()
	pending := q.Events
	for len(pending) > 0 {
		n, size := 0, 0
		for n < len(pending) && n < eventsBatch {
			raw, err := json.Marshal(pending[n])
			if err != nil || (n > 0 && size+len(raw)+1 > eventsBatchBytes) {
				break
			}
			size += len(raw) + 1
			n++
		}
		if n == 0 { // unmarshalable: never sendable
			done[pending[0].ID] = true
			pending = pending[1:]
			continue
		}
		r, err := a.callAPI(fctx, http.MethodPost, api, "/v1/events", token, map[string]any{"events": pending[:n]})
		switch {
		case err != nil:
			retry = now.Add(eventsBackoff)
			return
		case r.status == http.StatusBadRequest && badIndex(r.body, n) >= 0:
			// One event is never acceptable (a type an older API does not know): drop it, resend the rest.
			i := badIndex(r.body, n)
			done[pending[i].ID] = true
			pending = append(pending[:i:i], pending[i+1:]...)
		case r.status == http.StatusOK, r.status == http.StatusBadRequest: // stored, or never acceptable
			for _, ev := range pending[:n] {
				done[ev.ID] = true
			}
			pending = pending[n:]
		case r.status == http.StatusUnauthorized:
			hold = true
			return
		case r.status == http.StatusForbidden && r.code == "account_suspended":
			retry = now.Add(eventsBackoff)
			if a.Err != nil {
				fmt.Fprintln(a.Err, suspendedNotice)
			}
			return
		case r.status == http.StatusTooManyRequests:
			retry = now.Add(retryDelay(r.retryAfter))
			return
		default:
			retry = now.Add(eventsBackoff)
			return
		}
	}
}

// settleEvents rereads the queue, since the send took time and another command may have appended,
// removes exactly the events that were dealt with and saves the hold or backoff.
func settleEvents(done map[string]bool, hold bool, retry time.Time) {
	q, err := state.LoadEvents()
	if err != nil {
		return
	}
	kept := q.Events[:0]
	for _, ev := range q.Events {
		if !done[ev.ID] {
			kept = append(kept, ev)
		}
	}
	q.Events, q.Hold, q.RetryAfter = kept, q.Hold || hold, retry
	_ = state.SaveEvents(q)
}

// badIndex reads the failing event's index from a 400 answer; -1 when absent or outside the batch.
func badIndex(body []byte, n int) int {
	var e struct {
		Index *int `json:"index"`
	}
	if json.Unmarshal(body, &e) != nil || e.Index == nil || *e.Index < 0 || *e.Index >= n {
		return -1
	}
	return *e.Index
}

func retryDelay(ra time.Duration) time.Duration {
	if ra <= 0 {
		return eventsBackoff
	}
	return min(ra, maxBackoff)
}

// afterLogin runs once a login is confirmed by a /v1/me answer: it remembers the account, lifts a
// 401 hold, and drops queued events that belong to a different account first.
func (a *App) afterLogin(me *apiReply) {
	var who struct {
		ID string `json:"id"`
	}
	if me == nil || json.Unmarshal(me.body, &who) != nil || who.ID == "" {
		_ = state.RemoveAccount()
		if q, err := state.LoadEvents(); err == nil && len(q.Events) > 0 {
			q.Hold = true // whose events these are cannot be checked, so they wait for a login that can
			_ = state.SaveEvents(q)
		}
		return
	}
	_ = state.SaveAccount(who.ID)
	q, err := state.LoadEvents()
	if err != nil || len(q.Events) == 0 {
		return
	}
	if q.Account != "" && q.Account != who.ID {
		_ = state.RemoveEvents()
		return
	}
	q.Account, q.Hold = who.ID, false
	_ = state.SaveEvents(q)
}

// recordCheck queues the verdict of a full check run and returns the event's id ("" when events are off).
func (a *App) recordCheck(item string, code int, out []byte, took time.Duration) string {
	ms := min(max(int(took/time.Millisecond), 0), 86_400_000)
	ev := state.ProgressEvent{Type: "check.run", ItemID: item, Result: "pass", DurationMS: &ms, Output: string(out)}
	if code != 0 {
		ev.Result = "fail"
	}
	if p, t, ok := checkCounts(out); ok {
		ev.Passed, ev.Total = &p, &t
	}
	return a.recordEvent(ev)
}

// checkCounts reads the PASS and FAIL lines of a check's output.
func checkCounts(out []byte) (passed, total int, ok bool) {
	for line := range strings.SplitSeq(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "PASS"):
			passed++
			total++
		case strings.HasPrefix(line, "FAIL"):
			total++
		}
	}
	return passed, total, total > 0 && total <= 1000
}
