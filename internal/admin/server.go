// Package admin is the operator service: handlers and server-rendered pages. cmd/dojo-api never
// imports it (ADR 0002); a test in this package fails if it does.
package admin

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// Host is the only Host header the service answers. The operator reaches it through a loopback
// port-forward, so any other name means a DNS-rebinding page or a misrouted request (design 3.7).
const Host = "127.0.0.1:8090"

// healthTimeout bounds the database check so a hung database cannot hang the probe.
const healthTimeout = 2 * time.Second

// Readiness reports whether the service's dependencies are usable.
type Readiness interface {
	Ready(ctx context.Context) error
}

// New returns the operator router. Every request, known path or not, passes the host check first.
func New(ready Readiness) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", health(ready))
	return onlyHost(Host, mux)
}

// onlyHost answers 421 Misdirected Request with an empty body to any other Host. For an absolute-form
// request line Go sets r.Host from the URL; browsers never send that form, so rebinding stays blocked.
func onlyHost(host string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != host {
			w.WriteHeader(http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func health(ready Readiness) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), healthTimeout)
		defer cancel()
		status, body := http.StatusOK, "ok"
		if err := ready.Ready(ctx); err != nil {
			slog.Warn("health check failed", "error", err)
			status, body = http.StatusServiceUnavailable, "unavailable"
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": body})
	}
}
