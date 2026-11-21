//go:build integration

package server_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// One smoke test of the fake GitHub service: the web flow end to end, then revoke.
func TestFakeGitHubWebFlow(t *testing.T) {
	base := os.Getenv("DOJO_FAKE_GITHUB_URL")
	if base == "" {
		t.Fatal("DOJO_FAKE_GITHUB_URL is not set: run `make test-integration`")
	}
	const cb = "http://localhost:8080/v1/auth/github/callback"
	verifier := "integration-verifier-0123456789-abcdefghijklmnop"
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{"client_id": {"dojo-web-local"}, "redirect_uri": {cb}, "scope": {"read:user user:email"},
		"state": {"s"}, "login": {"ada"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"}}
	c := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	resp, err := c.Get(base + "/login/oauth/authorize?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if resp.StatusCode != http.StatusFound || err != nil || loc.Query().Get("code") == "" {
		t.Fatalf("authorize: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}

	form := url.Values{"client_id": {"dojo-web-local"}, "client_secret": {"web-local-only"}, "code": {loc.Query().Get("code")}, "code_verifier": {verifier}}
	req, _ := http.NewRequest(http.MethodPost, base+"/login/oauth/access_token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err = c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	err = json.NewDecoder(resp.Body).Decode(&tok)
	resp.Body.Close()
	if err != nil || tok.AccessToken == "" {
		t.Fatalf("access_token: %v %+v", err, tok)
	}

	userStatus := func() (int, map[string]any) {
		req, _ := http.NewRequest(http.MethodGet, base+"/user", nil)
		req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var u map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&u)
		return resp.StatusCode, u
	}
	if code, u := userStatus(); code != 200 || u["login"] != "ada" || u["two_factor_authentication"] != true {
		t.Fatalf("user: %d %v", code, u)
	}

	req, _ = http.NewRequest(http.MethodDelete, base+"/applications/dojo-web-local/token", strings.NewReader(`{"access_token":"`+tok.AccessToken+`"}`))
	req.SetBasicAuth("dojo-web-local", "web-local-only")
	resp, err = c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke: %d", resp.StatusCode)
	}
	if code, _ := userStatus(); code != http.StatusUnauthorized {
		t.Fatalf("after revoke: %d", code)
	}
}
