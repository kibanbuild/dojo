//go:build integration

package server_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kibanyadev/dojo/internal/server/repo"
)

// cliClient is a CLI on its own client IP: no cookie jar, bearer only.
type cliClient struct {
	t *testing.T
	b *browser
}

func newCLIClient(t *testing.T) *cliClient { return &cliClient{t: t, b: newBrowser(t)} }

func (c *cliClient) call(method, path, bearer string, body any) (int, string) {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, apiURL(c.t)+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := c.b.c.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

func fakeURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("DOJO_FAKE_GITHUB_URL")
	if u == "" {
		t.Fatal("DOJO_FAKE_GITHUB_URL is not set: run `make test-integration`")
	}
	return u
}

// deviceToken runs GitHub's device flow against the fake as login and returns the access token.
func deviceToken(t *testing.T, login, scope string) string {
	t.Helper()
	hc := &http.Client{Timeout: 10 * time.Second}
	post := func(path string, form url.Values, asJSON bool) map[string]any {
		var req *http.Request
		if asJSON {
			b, _ := json.Marshal(map[string]string{"client_id": form.Get("client_id"), "device_code": form.Get("device_code"), "grant_type": form.Get("grant_type")})
			req, _ = http.NewRequest(http.MethodPost, fakeURL(t)+path, bytes.NewReader(b))
			req.Header.Set("Content-Type", "application/json")
		} else {
			req, _ = http.NewRequest(http.MethodPost, fakeURL(t)+path, strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		req.Header.Set("Accept", "application/json")
		resp, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return out
	}
	dc := post("/login/device/code", url.Values{"client_id": {"dojo-web-local"}, "scope": {scope}}, false)
	device, user := dc["device_code"].(string), dc["user_code"].(string)
	if device == "" || user == "" {
		t.Fatalf("device code: %v", dc)
	}
	// The pending answer before approval is part of the flow.
	if got := post("/login/oauth/access_token", url.Values{"client_id": {"dojo-web-local"}, "device_code": {device}, "grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}}, false); got["error"] != "authorization_pending" {
		t.Fatalf("before approval: %v", got)
	}
	resp, err := hc.PostForm(fakeURL(t)+"/login/device", url.Values{"user_code": {user}, "login": {login}, "decision": {"approve"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("approve: %d", resp.StatusCode)
	}
	for range 10 {
		time.Sleep(1100 * time.Millisecond) // the fake's interval is 1s in the integration stack
		got := post("/login/oauth/access_token", url.Values{"client_id": {"dojo-web-local"}, "device_code": {device}, "grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}}, false)
		if tok, ok := got["access_token"].(string); ok {
			return tok
		}
		if got["error"] != "slow_down" && got["error"] != "authorization_pending" {
			t.Fatalf("poll: %v", got)
		}
	}
	t.Fatal("no access token after polling")
	return ""
}

// githubSays is the fake's check-token status for token, asked with the web client's credentials.
func githubSays(t *testing.T, token string) int {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"access_token": token})
	req, _ := http.NewRequest(http.MethodPost, fakeURL(t)+"/applications/dojo-web-local/token", bytes.NewReader(b))
	req.SetBasicAuth("dojo-web-local", "web-local-only")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func cliLoginBody(token string) map[string]string {
	return map[string]string{"access_token": token, "device_name": "integration laptop", "os": "linux/amd64", "cli_version": "v0.0.0-test"}
}

func signUp(t *testing.T, login string) {
	t.Helper()
	if resp := newBrowser(t).signIn(login, "", "age=on&terms=on", false); resp.Header.Get("Location") != "/app" {
		t.Fatalf("sign up %s: %d %q", login, resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestCLILoginEndToEnd(t *testing.T) {
	resetLearners(t, "1001")
	signUp(t, "ada")
	c := newCLIClient(t)

	ghTok := deviceToken(t, "ada", "read:user user:email")
	if githubSays(t, ghTok) != http.StatusOK {
		t.Fatal("the device token should be live before the login")
	}
	code, body := c.call(http.MethodPost, "/v1/auth/cli", "", cliLoginBody(ghTok))
	var out struct{ Token string }
	if code != http.StatusCreated || json.Unmarshal([]byte(body), &out) != nil || !strings.HasPrefix(out.Token, "dojo_") || len(out.Token) != 48 {
		t.Fatalf("login: %d %q", code, body)
	}
	if strings.Contains(body, ghTok) {
		t.Fatal("response holds the GitHub token")
	}

	// GitHub's token is gone: revoked by the API before it answered.
	if got := githubSays(t, ghTok); got != http.StatusNotFound {
		t.Fatalf("the GitHub token survived the login: check-token %d", got)
	}
	// Only the hash and the device fields are stored.
	sum := sha256.Sum256([]byte(out.Token))
	row := asOwner(t, `SELECT device_name || '|' || os || '|' || cli_version || '|' || (revoked_at IS NULL) FROM cli_tokens WHERE token_hash = '\x`+hex.EncodeToString(sum[:])+`'`)
	if row != "integration laptop|linux/amd64|v0.0.0-test|true" {
		t.Fatalf("stored row: %q", row)
	}
	if n := asOwner(t, "SELECT count(*) FROM cli_tokens WHERE position(convert_to('"+out.Token+"', 'UTF8') in token_hash) > 0"); n != "0" {
		t.Fatalf("raw token in the table: %s", n)
	}

	// The same GitHub token cannot log in twice.
	if code, body := c.call(http.MethodPost, "/v1/auth/cli", "", cliLoginBody(ghTok)); code != http.StatusUnauthorized || body != "{\"error\":\"github_rejected\"}\n" {
		t.Fatalf("replayed GitHub token: %d %q", code, body)
	}

	code, body = c.call(http.MethodGet, "/v1/me", out.Token, nil)
	var me map[string]any
	if code != http.StatusOK || json.Unmarshal([]byte(body), &me) != nil || me["github_login"] != "ada" || me["email"] != "ada@example.test" {
		t.Fatalf("me with bearer: %d %q", code, body)
	}
	// Bearer calls need no CSRF header; a bearer cannot use the web sign-out.
	if code, _ := c.call(http.MethodPost, "/v1/auth/logout", out.Token, nil); code != http.StatusUnauthorized {
		t.Fatalf("web logout with a bearer: %d", code)
	}
	if code, _ := c.call(http.MethodGet, "/v1/me", out.Token+"x", nil); code != http.StatusUnauthorized {
		t.Fatalf("altered token: %d", code)
	}
	if code, _ := c.call(http.MethodGet, "/v1/me", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("no credential: %d", code)
	}

	if code, body := c.call(http.MethodDelete, "/v1/auth/cli", out.Token, nil); code != http.StatusNoContent || body != "" {
		t.Fatalf("logout: %d %q", code, body)
	}
	if code, _ := c.call(http.MethodGet, "/v1/me", out.Token, nil); code != http.StatusUnauthorized {
		t.Fatalf("after logout: %d", code)
	}
	if code, _ := c.call(http.MethodDelete, "/v1/auth/cli", out.Token, nil); code != http.StatusUnauthorized {
		t.Fatalf("second logout: %d", code)
	}
	if got := asOwner(t, `SELECT count(*) FROM cli_tokens WHERE token_hash = '\x`+hex.EncodeToString(sum[:])+`' AND revoked_at IS NOT NULL`); got != "1" {
		t.Fatalf("revoked rows: %s", got)
	}
}

func TestCLILoginUnknownAccountNeedsTheWeb(t *testing.T) {
	resetLearners(t, "1002")
	c := newCLIClient(t)
	before := asOwner(t, "SELECT count(*) FROM cli_tokens")
	ghTok := deviceToken(t, "bob", "read:user user:email")
	code, body := c.call(http.MethodPost, "/v1/auth/cli", "", cliLoginBody(ghTok))
	if code != http.StatusForbidden || body != "{\"error\":\"signup_required\"}\n" {
		t.Fatalf("got %d %q", code, body)
	}
	if learnerCount(t, "1002") != "0" || asOwner(t, "SELECT count(*) FROM cli_tokens") != before {
		t.Fatal("a refused login stored a learner or a token")
	}
	// Nothing of GitHub's outlives the refusal.
	if got := githubSays(t, ghTok); got != http.StatusNotFound {
		t.Fatalf("the GitHub token survived: %d", got)
	}
}

func TestCLILoginSuspendedLearner(t *testing.T) {
	resetLearners(t, "1002")
	signUp(t, "bob")
	c := newCLIClient(t)
	code, body := c.call(http.MethodPost, "/v1/auth/cli", "", cliLoginBody(deviceToken(t, "bob", "read:user user:email")))
	var out struct{ Token string }
	if code != http.StatusCreated || json.Unmarshal([]byte(body), &out) != nil {
		t.Fatalf("login: %d %q", code, body)
	}
	asOwner(t, "UPDATE learners SET suspended_at = now() WHERE github_id = 1002")

	if code, body := c.call(http.MethodGet, "/v1/me", out.Token, nil); code != http.StatusForbidden || body != "{\"error\":\"account_suspended\"}\n" {
		t.Fatalf("suspended me: %d %q", code, body)
	}
	if code, body := c.call(http.MethodPost, "/v1/auth/cli", "", cliLoginBody(deviceToken(t, "bob", "read:user user:email"))); code != http.StatusForbidden || body != "{\"error\":\"account_suspended\"}\n" {
		t.Fatalf("suspended login: %d %q", code, body)
	}
	if code, _ := c.call(http.MethodDelete, "/v1/auth/cli", out.Token, nil); code != http.StatusNoContent {
		t.Fatalf("a suspended learner must still be able to log out: %d", code)
	}
}

// A token the fake's other OAuth client issued is not ours: the check-token call says 404 for it.
func TestCLILoginTokenOfAnotherApp(t *testing.T) {
	resetLearners(t, "1001")
	signUp(t, "ada")
	hc := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	q := url.Values{"client_id": {"dojo-admin-local"}, "redirect_uri": {"http://127.0.0.1:8090/auth/github/callback"}, "scope": {"read:user"}, "state": {"s"}, "login": {"ada"}}
	resp, err := hc.Get(fakeURL(t) + "/login/oauth/authorize?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc, _ := url.Parse(resp.Header.Get("Location"))
	if loc == nil || loc.Query().Get("code") == "" {
		t.Skipf("the fake needs PKCE for this client: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	form := url.Values{"client_id": {"dojo-admin-local"}, "client_secret": {"admin-local-only"}, "code": {loc.Query().Get("code")}}
	req, _ := http.NewRequest(http.MethodPost, fakeURL(t)+"/login/oauth/access_token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err = hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&tok)
	resp.Body.Close()
	if tok.AccessToken == "" {
		t.Fatalf("no token from the other client: %d", resp.StatusCode)
	}
	code, body := newCLIClient(t).call(http.MethodPost, "/v1/auth/cli", "", cliLoginBody(tok.AccessToken))
	if code != http.StatusUnauthorized || body != "{\"error\":\"github_rejected\"}\n" {
		t.Fatalf("got %d %q", code, body)
	}
}

func TestCLILoginBadInput(t *testing.T) {
	c := newCLIClient(t)
	for name, body := range map[string]any{
		"no token":    map[string]string{"device_name": "x", "os": "linux", "cli_version": "1"},
		"long device": map[string]string{"access_token": "gho_x", "device_name": strings.Repeat("d", 65), "os": "linux", "cli_version": "1"},
		"bad os":      map[string]string{"access_token": "gho_x", "device_name": "x", "os": "li nux!", "cli_version": "1"},
	} {
		if code, out := c.call(http.MethodPost, "/v1/auth/cli", "", body); code != http.StatusBadRequest || out != "{\"error\":\"bad_request\"}\n" {
			t.Fatalf("%s: %d %q", name, code, out)
		}
	}
	if code, _ := c.call(http.MethodPost, "/v1/auth/cli", "", map[string]string{"access_token": "gho_unknown", "device_name": "x", "os": "linux", "cli_version": "1"}); code != http.StatusUnauthorized {
		t.Fatalf("unknown GitHub token: %d", code)
	}
}

// The login route sits under /v1/auth, so it shares the per-IP budget: 30 requests, then 429.
func TestCLILoginIsRateLimited(t *testing.T) {
	c := newCLIClient(t)
	var last int
	for range 31 {
		last, _ = c.call(http.MethodPost, "/v1/auth/cli", "", map[string]string{})
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("31st request: %d, want 429", last)
	}
}

func TestCLITokenQueries(t *testing.T) {
	resetLearners(t, "4243")
	ctx := context.Background()
	q := repo.New(apiConn(t))
	org, err := q.GetOrgBySlug(ctx, "public")
	if err != nil {
		t.Fatal(err)
	}
	now := pgtype.Timestamptz{Time: time.Now(), Valid: true}
	l, err := q.UpsertLearner(ctx, repo.UpsertLearnerParams{OrgID: org.ID, GithubID: 4243, GithubLogin: "Cli", Username: "clitok", Language: "en",
		AgeConfirmedAt: now, TermsAcceptedAt: now, TermsVersion: "2026-11-20", UsageStats: true})
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("cli"))
	tok, err := q.InsertCLIToken(ctx, repo.InsertCLITokenParams{OrgID: org.ID, LearnerID: l.ID, TokenHash: hash[:], DeviceName: "d", Os: "linux", CliVersion: "1"})
	if err != nil {
		t.Fatal(err)
	}
	active, err := q.GetActiveCLIToken(ctx, hash[:])
	if err != nil || active.ID != tok.ID || active.LearnerID != l.ID || active.SuspendedAt.Valid {
		t.Fatalf("active: %v %+v", err, active)
	}
	if err := q.TouchCLIToken(ctx, tok.ID); err != nil {
		t.Fatal(err)
	}
	if again, err := q.GetActiveCLIToken(ctx, hash[:]); err != nil || !again.LastUsedAt.Time.After(active.LastUsedAt.Time) {
		t.Fatalf("touch did not move last_used_at: %v %+v", err, again)
	}
	// A CLI token has no expiry: age alone never deactivates it.
	asOwner(t, "UPDATE cli_tokens SET last_used_at = now() - interval '400 days' WHERE learner_id = '"+l.ID.String()+"'")
	if _, err := q.GetActiveCLIToken(ctx, hash[:]); err != nil {
		t.Fatalf("an old token must stay active: %v", err)
	}
	if _, err := q.InsertCLIToken(ctx, repo.InsertCLITokenParams{OrgID: org.ID, LearnerID: l.ID, TokenHash: hash[:], DeviceName: "d", Os: "linux", CliVersion: "1"}); err == nil {
		t.Fatal("a duplicate hash was accepted")
	}
	if _, err := q.InsertCLIToken(ctx, repo.InsertCLITokenParams{OrgID: org.ID, LearnerID: l.ID, TokenHash: make([]byte, 31), DeviceName: "d", Os: "linux", CliVersion: "1"}); err == nil {
		t.Fatal("a short hash was accepted")
	}
	if _, err := q.InsertCLIToken(ctx, repo.InsertCLITokenParams{OrgID: org.ID, LearnerID: l.ID, TokenHash: make([]byte, 32), DeviceName: "", Os: "linux", CliVersion: "1"}); err == nil {
		t.Fatal("an empty device name was accepted")
	}
	if err := q.RevokeCLIToken(ctx, tok.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := q.GetActiveCLIToken(ctx, hash[:]); err != pgx.ErrNoRows {
		t.Fatalf("revoked: %v", err)
	}
	asOwner(t, "DELETE FROM learners WHERE github_id = 4243")
	if out := asOwner(t, "SELECT count(*) FROM cli_tokens WHERE learner_id = '"+l.ID.String()+"'"); out != "0" {
		t.Fatalf("tokens after cascade: %q", out)
	}
}
