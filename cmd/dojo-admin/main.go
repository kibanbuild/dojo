// Command dojo-admin serves the operator service (ADR 0002). It is reached only through a loopback
// port-forward and never sits behind the public edge.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kibanyadev/dojo/internal/admin"
	"github.com/kibanyadev/dojo/internal/dbpool"
	"github.com/kibanyadev/dojo/internal/github"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("dojo-admin failed", "error", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	addr := envOr("LISTEN_ADDR", ":8090")
	switch {
	case len(args) == 1 && args[0] == "healthcheck":
		return healthcheck(addr)
	case len(args) > 0 && args[0] == "bootstrap":
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		return bootstrap(ctx, args[1:], os.Getenv, os.Stdout)
	case len(args) > 0:
		return fmt.Errorf("unknown arguments %q: usage: dojo-admin [healthcheck | bootstrap <github-login>]", args)
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	// The operator service has its own OAuth App, so the public app's credentials never mint an admin session.
	ghCfg, err := github.LoadConfig(os.Getenv, os.Getenv("DOJO_ENV") == "local")
	if err != nil {
		return err
	}
	ghCfg.Scopes = admin.Scope
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := dbpool.New(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("open pool: %w", err)
	}
	defer pool.Close()

	srv := &http.Server{
		Addr:              addr,
		Handler:           admin.New(pingReady{pool}, admin.Deps{GitHub: github.New(ghCfg), Store: admin.PGStore{Pool: pool}, Learners: admin.PGLearners{Pool: pool}}),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	slog.Info("listening", "addr", addr)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// pingReady is ready when the pool can open a connection as dojo_admin.
type pingReady struct{ pool *pgxpool.Pool }

func (p pingReady) Ready(ctx context.Context) error { return p.pool.Ping(ctx) }

// healthcheck probes the local server; the scratch image has no curl for the compose check.
// It sends the one Host the service answers, as the operator's port-forward does.
func healthcheck(addr string) error {
	if addr[0] == ':' {
		addr = "127.0.0.1" + addr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/healthz", nil)
	if err != nil {
		return err
	}
	req.Host = admin.Host
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health returned %d", resp.StatusCode)
	}
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
