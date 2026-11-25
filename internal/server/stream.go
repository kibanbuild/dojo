package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	streamPath             = "/v1/stream"
	defaultStreamHeartbeat = 25 * time.Second
)

// streamFrame names a NOTIFY message for the stream: a message with a string `event` key is that
// event, sent without the key; one without it is a progress message as before (design 3.9).
func streamFrame(msg []byte) (name string, data []byte) {
	var m map[string]json.RawMessage
	if json.Unmarshal(msg, &m) != nil {
		return "progress", msg
	}
	var ev string
	if json.Unmarshal(m["event"], &ev) != nil || ev == "" {
		return "progress", msg
	}
	delete(m, "event")
	b, err := json.Marshal(m)
	if err != nil {
		return "progress", msg
	}
	return ev, b
}

// ParseStreamHeartbeat reads STREAM_HEARTBEAT_SECONDS: empty is 25 s, shorter values are for tests.
func ParseStreamHeartbeat(s string) (time.Duration, error) {
	if s == "" {
		return defaultStreamHeartbeat, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 55 {
		return 0, fmt.Errorf("STREAM_HEARTBEAT_SECONDS must be 1 to 55, got %q", s)
	}
	return time.Duration(n) * time.Second, nil
}

// stream is GET /v1/stream (design 3.8): server-sent events for the cookie session only.
func (a Auth) stream(w http.ResponseWriter, r *http.Request) {
	p, ok := principalOf(r.Context())
	switch {
	case !ok:
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	case p.bearer:
		writeError(w, http.StatusForbidden, "forbidden")
		return
	case p.suspended:
		writeError(w, http.StatusForbidden, "account_suspended")
		return
	}
	sub, err := a.Stream.Subscribe(r.Context(), p.learner)
	switch {
	case errors.Is(err, ErrTooManyStreams):
		writeError(w, http.StatusTooManyRequests, "too_many_streams")
		return
	case err != nil:
		if r.Context().Err() == nil {
			slog.Error("stream: subscribe failed", "error", err)
			writeError(w, http.StatusServiceUnavailable, "server_error")
		}
		return
	}
	defer a.Stream.Unsubscribe(sub)

	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{}) // the server's write timeout must not cut the stream
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	send := func(format string, args ...any) bool {
		if _, err := fmt.Fprintf(w, format, args...); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !send("retry: 3000\n\nevent: ready\ndata: {}\n\n") {
		return
	}

	every := a.StreamHeartbeat
	if every <= 0 {
		every = defaultStreamHeartbeat
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		var ok bool
		select {
		case <-r.Context().Done():
			return
		case <-a.Stream.Done():
			return
		case msg := <-sub.C:
			name, data := streamFrame(msg)
			ok = send("event: %s\ndata: %s\n\n", name, data)
		case <-sub.Wake:
			ok = !sub.TakeResync() || send("event: resync\ndata: {}\n\n")
		case <-tick.C:
			if !a.sessionLive(r) {
				return
			}
			ok = send("event: heartbeat\ndata: {\"at\":%q}\n\n", time.Now().UTC().Format(time.RFC3339))
		}
		if !ok {
			return
		}
	}
}

// sessionLive re-checks the cookie session; a database error keeps the stream open.
func (a Auth) sessionLive(r *http.Request) bool {
	c, err := r.Cookie(SessionCookie)
	if err != nil {
		return false
	}
	row, err := a.Store.GetActiveSession(r.Context(), HashToken(c.Value))
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return false
	case err != nil:
		if r.Context().Err() == nil {
			slog.Warn("stream: session re-check failed", "error", err)
		}
		return true
	}
	return !row.SuspendedAt.Valid
}
