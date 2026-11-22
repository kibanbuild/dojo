//go:build integration

package github_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/kibanbuild/dojo/internal/github"
)

// signIn runs the code path of the callback against the compose fake: authorize with login=, then exchange.
func signIn(t *testing.T, c *github.Client, base, login string) github.Token {
	t.Helper()
	const cb = "http://localhost:8080/v1/auth/github/callback"
	verifier, challenge, err := github.NewPKCE()
	if err != nil {
		t.Fatal(err)
	}
	u := c.AuthorizeURL("st", challenge, cb) + "&login=" + login
	hc := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := hc.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || resp.StatusCode != http.StatusFound || loc.Query().Get("code") == "" || loc.Query().Get("state") != "st" {
		t.Fatalf("authorize: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	tok, err := c.Exchange(context.Background(), loc.Query().Get("code"), verifier, cb)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func newClient(t *testing.T) (*github.Client, string) {
	base := os.Getenv("DOJO_FAKE_GITHUB_URL")
	if base == "" {
		t.Fatal("DOJO_FAKE_GITHUB_URL is not set: run `make test-integration`")
	}
	return github.New(github.Config{ClientID: "dojo-web-local", ClientSecret: "web-local-only", AuthorizeURL: base, OAuthURL: base, APIURL: base}), base
}

func TestFakeGitHubSignInAndRevoke(t *testing.T) {
	c, base := newClient(t)
	ctx := context.Background()
	tok := signIn(t, c, base, "ada")
	if !github.HasScope(tok.Scopes, "user:email") {
		t.Fatalf("scopes %v", tok.Scopes)
	}
	u, err := c.User(ctx, tok.AccessToken)
	if err != nil || u.ID != 1001 || u.Login != "ada" || !github.HasScope(u.Scopes, "read:user") {
		t.Fatalf("user: %+v %v", u, err)
	}
	if email, err := c.Emails(ctx, tok.AccessToken); err != nil || email != "ada@example.test" {
		t.Fatalf("emails: %q %v", email, err)
	}
	if err := c.Revoke(ctx, tok.AccessToken); err != nil {
		t.Fatal(err)
	}
	if _, err := c.User(ctx, tok.AccessToken); !errors.Is(err, github.ErrRejected) {
		t.Fatalf("after revoke: %v", err)
	}
}

func TestFakeGitHubUnverifiedEmailIsNone(t *testing.T) {
	c, base := newClient(t)
	tok := signIn(t, c, base, "cy")
	if email, err := c.Emails(context.Background(), tok.AccessToken); err != nil || email != "" {
		t.Fatalf("emails: %q %v", email, err)
	}
}

func TestFakeGitHubReusedCodeIsRejected(t *testing.T) {
	c, _ := newClient(t)
	if _, err := c.Exchange(context.Background(), "nope", "v", "http://localhost:8080/v1/auth/github/callback"); !errors.Is(err, github.ErrRejected) {
		t.Fatalf("%v", err)
	}
}
