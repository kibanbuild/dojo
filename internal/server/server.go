// Package server holds the HTTP routes of the Dojo API.
package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// healthTimeout bounds the database check so a hung database cannot hang the probe.
const healthTimeout = 2 * time.Second

// Readiness reports whether the API's dependencies are usable.
type Readiness interface {
	Ready(ctx context.Context) error
}

// New returns the API router.
func New(ready Readiness, auth Auth) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", health(ready))
	mux.HandleFunc("GET /v1/me", requireSession(auth.me))
	return auth.Session(CSRF(mux))
}

func health(ready Readiness) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), healthTimeout)
		defer cancel()
		if err := ready.Ready(ctx); err != nil {
			slog.Warn("health check failed", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
