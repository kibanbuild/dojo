// Command fake-github serves the local stand-in for GitHub's OAuth web flow and user endpoints.
package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kibanbuild/dojo/internal/fakegithub"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fake-github failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	clients, err := parseClients(os.Getenv("FAKE_GITHUB_CLIENTS"))
	if err != nil {
		return err
	}
	if err := enableDevice(clients, os.Getenv("FAKE_GITHUB_DEVICE_CLIENTS")); err != nil {
		return err
	}
	interval := 5 * time.Second
	if v := os.Getenv("FAKE_GITHUB_DEVICE_INTERVAL"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return fmt.Errorf("FAKE_GITHUB_DEVICE_INTERVAL must be whole seconds >= 1, got %q", v)
		}
		interval = time.Duration(n) * time.Second
	}
	rateLimit := 0
	if v := os.Getenv("FAKE_GITHUB_RATE_LIMIT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return fmt.Errorf("FAKE_GITHUB_RATE_LIMIT must be a whole number >= 0, got %q", v)
		}
		rateLimit = n
	}
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8081"
	}
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		return healthcheck(addr)
	}
	srv := &http.Server{
		Addr: addr,
		Handler: fakegithub.New(fakegithub.Config{Clients: clients, Users: fakegithub.DefaultUsers(),
			PublicURL: os.Getenv("FAKE_GITHUB_PUBLIC_URL"), DeviceInterval: interval, RateLimit: rateLimit}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	slog.Info("listening", "addr", addr)
	return srv.ListenAndServe()
}

// parseClients reads "id:secret:redirect1|redirect2;..." (redirect URIs hold colons, so split into three).
func parseClients(s string) ([]fakegithub.Client, error) {
	if s == "" {
		return nil, errors.New("FAKE_GITHUB_CLIENTS is required (see .env.example)")
	}
	var out []fakegithub.Client
	for _, e := range strings.Split(s, ";") {
		p := strings.SplitN(e, ":", 3)
		if len(p) != 3 || p[0] == "" || p[1] == "" || p[2] == "" {
			return nil, fmt.Errorf("bad FAKE_GITHUB_CLIENTS entry %q", e)
		}
		out = append(out, fakegithub.Client{ID: p[0], Secret: p[1], RedirectURIs: strings.Split(p[2], "|")})
	}
	return out, nil
}

// enableDevice turns the device flow on for the listed client ids (GitHub enables it per OAuth App).
func enableDevice(clients []fakegithub.Client, list string) error {
	for _, id := range strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ' ' }) {
		i := slices.IndexFunc(clients, func(c fakegithub.Client) bool { return c.ID == id })
		if i < 0 {
			return fmt.Errorf("FAKE_GITHUB_DEVICE_CLIENTS names unknown client %q", id)
		}
		clients[i].DeviceFlow = true
	}
	return nil
}

// healthcheck probes the local server; the scratch image has no curl for the compose check.
func healthcheck(addr string) error {
	if addr[0] == ':' {
		addr = "127.0.0.1" + addr
	}
	c := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get("http://" + addr + "/login/oauth/authorize") //nolint:gosec // addr is our own listen address
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("healthcheck: status %d", resp.StatusCode)
	}
	return nil
}
