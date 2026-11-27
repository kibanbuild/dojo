//go:build integration

package server_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kibanyadev/dojo/internal/cli"
	"github.com/kibanyadev/dojo/internal/state"
)

// syncBuf is a buffer the test reads while the CLI writes.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

var userCodeRE = regexp.MustCompile(`\b[A-Z]{4}-[A-Z]{4}\b`)

// runCLI runs the real dojo command in-process against the stack: the API directly on its own client
// IP (the edge replaces X-Forwarded-For with one peer address, which would share a rate-limit budget
// across the whole run) and the fake GitHub. approveAs approves the device code as that fake user once
// the CLI has printed it ("" leaves it pending, "deny" denies).
func runCLI(t *testing.T, approveAs string, args ...string) (string, error) {
	t.Helper()
	return runCLIWith(t, approveAs, nil, args...)
}

// runCLIWith is runCLI with a hook to set more of the app, such as the doctor's probe and backend.
func runCLIWith(t *testing.T, approveAs string, setup func(*cli.App), args ...string) (string, error) {
	t.Helper()
	out := &syncBuf{}
	app := &cli.App{
		Out: out, Err: out, Now: time.Now,
		HTTP:  &http.Client{Timeout: 15 * time.Second, Transport: ipTransport{newBrowser(t).ip}},
		Login: cli.LoginConfig{APIURL: apiURL(t), GitHubURL: fakeURL(t), GitHubOAuth: "dojo-web-local"},
	}
	if setup != nil {
		setup(app)
	}
	done := make(chan struct{})
	defer close(done)
	if approveAs != "" {
		go func() {
			for {
				select {
				case <-done:
					return
				case <-time.After(100 * time.Millisecond):
				}
				if code := userCodeRE.FindString(out.String()); code != "" {
					decision, login := "approve", approveAs
					if approveAs == "deny" {
						decision, login = "deny", "ada"
					}
					resp, err := http.PostForm(fakeURL(t)+"/login/device", url.Values{"user_code": {code}, "login": {login}, "decision": {decision}})
					if err == nil {
						resp.Body.Close()
					}
					return
				}
			}
		}()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	err := app.Run(ctx, args)
	return out.String(), err
}

func TestCLICommandsLoginAndLogoutEndToEnd(t *testing.T) {
	resetLearners(t, "1001")
	signUp(t, "ada")
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("DOJO_HOME", home)

	out, err := runCLI(t, "ada", "login")
	if err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Logged in as ") {
		t.Fatalf("output: %s", out)
	}
	tok, err := state.LoadToken()
	if err != nil || !strings.HasPrefix(tok, "dojo_") || len(tok) != 48 {
		t.Fatalf("stored token %q: %v", tok, err)
	}
	if strings.Contains(out, tok) {
		t.Fatal("the token was printed")
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(filepath.Join(home, "token")); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("token file: %v %v", fi, err)
		}
	}
	c := newCLIClient(t)
	if code, body := c.call(http.MethodGet, "/v1/me", tok, nil); code != http.StatusOK || !strings.Contains(body, `"github_login":"ada"`) {
		t.Fatalf("me: %d %q", code, body)
	}

	// Logged in already: says who, no new flow (nothing approves a code, so a flow would hang).
	if out, err := runCLI(t, "", "login"); err != nil || !strings.Contains(out, "Already logged in as ") {
		t.Fatalf("second login: %v\n%s", err, out)
	}

	if out, err := runCLI(t, "", "logout"); err != nil || !strings.Contains(out, "Logged out") {
		t.Fatalf("logout: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(home, "token")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("token file after logout: %v", err)
	}
	if code, _ := c.call(http.MethodGet, "/v1/me", tok, nil); code != http.StatusUnauthorized {
		t.Fatalf("revoked token still works: %d", code)
	}
}

func TestCLICommandsLoginUnknownAccountAndDenied(t *testing.T) {
	resetLearners(t, "1001", "1002")
	t.Setenv("DOJO_HOME", filepath.Join(t.TempDir(), "home"))

	_, err := runCLI(t, "bob", "login")
	if err == nil || !strings.Contains(err.Error(), "sign up at ") {
		t.Fatalf("unknown account: %v", err)
	}
	if _, err := state.LoadToken(); !errors.Is(err, state.ErrNoToken) {
		t.Fatalf("a refused login stored a token: %v", err)
	}
	if _, err := runCLI(t, "deny", "login"); err == nil || !strings.Contains(err.Error(), "cancelled on GitHub") {
		t.Fatalf("denied: %v", err)
	}
}
