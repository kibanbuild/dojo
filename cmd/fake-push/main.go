// Command fake-push is the local stand-in for a Web Push service: it records what arrives so tests can read it.
package main

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"
)

const (
	maxBody    = 16 << 10
	maxPerHook = 100
	maxTokens  = 1000
	maxStored  = 64 << 20
)

type push struct {
	Headers map[string][]string `json:"headers"`
	Body    []byte              `json:"body"`
}

type store struct {
	mu     sync.Mutex
	pushes map[string][]push
	gone   map[string]bool
	bytes  int
}

func newHandler() http.Handler {
	s := &store{pushes: map[string][]push{}, gone: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(http.ResponseWriter, *http.Request) {})
	mux.HandleFunc("POST /push/{token}", s.receive)
	mux.HandleFunc("POST /_fake/gone/{token}", func(w http.ResponseWriter, r *http.Request) {
		token := r.PathValue("token")
		s.mu.Lock()
		defer s.mu.Unlock()
		if !s.gone[token] && len(s.gone) >= maxTokens {
			http.Error(w, "too many tokens", http.StatusTooManyRequests)
			return
		}
		s.gone[token] = true
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /_fake/pushes/{token}", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		list := append([]push{}, s.pushes[r.PathValue("token")]...)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(list)
	})
	return mux
}

func (s *store) receive(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	token := r.PathValue("token")
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gone[token] {
		w.WriteHeader(http.StatusGone)
		return
	}
	_, known := s.pushes[token]
	if len(s.pushes[token]) >= maxPerHook || s.bytes+len(body) > maxStored || (!known && len(s.pushes) >= maxTokens) {
		http.Error(w, "too many pushes", http.StatusTooManyRequests)
		return
	}
	s.bytes += len(body)
	s.pushes[token] = append(s.pushes[token], push{Headers: r.Header.Clone(), Body: body})
	w.WriteHeader(http.StatusCreated)
}

func main() {
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		if err := healthcheck(addr); err != nil {
			slog.Error("fake-push unhealthy", "error", err)
			os.Exit(1)
		}
		return
	}
	srv := &http.Server{Addr: addr, Handler: newHandler(), ReadHeaderTimeout: 5 * time.Second}
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		slog.Error("fake-push failed", "error", err)
		os.Exit(1)
	}
}

// healthcheck probes the local server; the scratch image has no curl for the compose check.
func healthcheck(addr string) error {
	if addr[0] == ':' {
		addr = "127.0.0.1" + addr
	}
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Get("http://" + addr + "/healthz") //nolint:gosec // addr is our own listen address
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("healthcheck: status " + resp.Status)
	}
	return nil
}
