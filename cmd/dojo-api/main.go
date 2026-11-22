// Command dojo-api serves the Dojo HTTP API.
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

	"github.com/kibanbuild/dojo/internal/server"
	"github.com/kibanbuild/dojo/internal/server/repo"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("dojo-api failed", "error", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	addr := envOr("LISTEN_ADDR", ":8080")
	switch {
	case len(args) == 1 && args[0] == "healthcheck":
		return healthcheck(addr)
	case len(args) > 0:
		return fmt.Errorf("unknown arguments %q: usage: dojo-api [healthcheck]", args)
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	secure, err := server.ParseOrigin(os.Getenv("DOJO_ENV"), os.Getenv("PUBLIC_ORIGIN"))
	if err != nil {
		return err
	}
	hops, err := server.ParseTrustedHops(os.Getenv("TRUSTED_PROXY_HOPS"))
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	store, err := repo.Open(ctx, dbURL)
	if err != nil {
		return err
	}
	defer store.Close()

	srv := &http.Server{
		Addr:              addr,
		Handler:           server.New(store, server.Auth{Store: store.Queries(), Secure: secure}, server.NewRateLimits(hops)),
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

// healthcheck probes the local server; the scratch image has no curl for the compose check.
func healthcheck(addr string) error {
	if addr[0] == ':' {
		addr = "127.0.0.1" + addr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/v1/health", nil)
	if err != nil {
		return err
	}
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
