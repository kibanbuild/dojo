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
// Without a GitHub client and a store the sign-in routes are not mounted.
func New(ready Readiness, d Deps) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", health(ready))
	if d.GitHub != nil && d.Store != nil {
		s := &signIn{Deps: d}
		mux.HandleFunc("GET /signin", s.page)
		mux.HandleFunc("POST "+startPath, s.start)
		mux.HandleFunc("GET "+callbackPath, s.callback)
		mux.HandleFunc("GET /{$}", s.authenticated(s.landing))
		mux.HandleFunc("POST /signout", s.authenticated(s.signout))
	}
	return onlyHost(Host, secureHeaders(mux))
}

// secureHeaders marks every answer uncacheable and unframeable, with a CSP that allows no script,
// style or subresource: the pages are plain HTML. form-action is left open on purpose, the start
// form is answered with a redirect to GitHub.
func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
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
