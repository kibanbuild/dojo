package github

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// Scopes are requested at sign-in: the email needs user:email (design 3.1).
	Scopes    = "read:user user:email"
	maxBody   = 256 << 10
	userAgent = "kiban-dojo-api"
)

var (
	// ErrRejected means GitHub refused on the user's account: denied, bad or expired code, bad token.
	ErrRejected = errors.New("github rejected the request")
	// ErrUnavailable means GitHub failed, was unreachable, or answered something unusable (including our own misconfiguration).
	ErrUnavailable = errors.New("github unavailable")
)

// Client talks to GitHub. Create it with New.
type Client struct {
	cfg  Config
	http *http.Client
}

// New returns a client with a bounded timeout that never follows redirects, so credentials stay on the configured hosts.
func New(cfg Config) *Client {
	return &Client{cfg: cfg, http: &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// String, GoString and LogValue keep the secret out of fmt and slog output of a *Client.
func (c *Client) String() string   { return "github.Client{" + c.cfg.String() + "}" }
func (c *Client) GoString() string { return c.String() }

// LogValue implements slog.LogValuer.
func (c *Client) LogValue() slog.Value { return slog.StringValue(c.String()) }

// NewPKCE returns an RFC 7636 verifier (32 random bytes, base64url) and its S256 challenge.
func NewPKCE() (verifier, challenge string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

// AuthorizeURL is the page the browser is sent to.
func (c *Client) AuthorizeURL(state, codeChallenge, redirectURI string) string {
	q := url.Values{
		"client_id": {c.cfg.ClientID}, "redirect_uri": {redirectURI}, "scope": {Scopes}, "state": {state},
		"code_challenge": {codeChallenge}, "code_challenge_method": {"S256"},
	}
	return c.cfg.AuthorizeURL + "/login/oauth/authorize?" + q.Encode()
}

// Token is the result of a code exchange.
type Token struct {
	AccessToken string
	Scopes      []string
}

// HasScope reports whether the grant includes scope.
func HasScope(scopes []string, scope string) bool {
	for _, s := range scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// Exchange trades an authorization code for an access token.
func (c *Client) Exchange(ctx context.Context, code, verifier, redirectURI string) (Token, error) {
	form := url.Values{"client_id": {c.cfg.ClientID}, "client_secret": {string(c.cfg.ClientSecret)}, "code": {code},
		"code_verifier": {verifier}, "redirect_uri": {redirectURI}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.OAuthURL+"/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	status, _, body, err := c.do(req)
	if err != nil {
		return Token{}, fmt.Errorf("exchange: %w", err)
	}
	var out struct {
		AccessToken string `json:"access_token"`
		Scope       string `json:"scope"`
		Error       string `json:"error"`
	}
	if status != http.StatusOK || json.Unmarshal(body, &out) != nil {
		return Token{}, fmt.Errorf("exchange: %w: status %d", ErrUnavailable, status)
	}
	switch {
	case out.Error == "bad_verification_code" || out.Error == "access_denied":
		return Token{}, fmt.Errorf("exchange: %w: %s", ErrRejected, out.Error)
	case out.Error != "":
		return Token{}, fmt.Errorf("exchange: %w: %s", ErrUnavailable, safe(out.Error))
	case out.AccessToken == "":
		return Token{}, fmt.Errorf("exchange: %w: no access token", ErrUnavailable)
	}
	return Token{AccessToken: out.AccessToken, Scopes: splitScopes(out.Scope)}, nil
}

// User is the signed-in GitHub account. Email is the public profile email, often empty.
type User struct {
	ID     int64
	Login  string
	Name   string
	Email  string
	Scopes []string // from X-OAuth-Scopes
}

// User reads the account the token belongs to.
func (c *Client) User(ctx context.Context, token string) (User, error) {
	status, h, body, err := c.api(ctx, http.MethodGet, "/user", token)
	if err != nil {
		return User{}, fmt.Errorf("user: %w", err)
	}
	if err := apiStatus(status, false); err != nil {
		return User{}, fmt.Errorf("user: %w", err)
	}
	var out struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	if json.Unmarshal(body, &out) != nil || out.ID == 0 || out.Login == "" {
		return User{}, fmt.Errorf("user: %w: unusable body", ErrUnavailable)
	}
	return User{out.ID, out.Login, out.Name, out.Email, splitScopes(h.Get("X-OAuth-Scopes"))}, nil
}

// Emails returns the primary verified email, or "" when there is none; an unverified one is never returned.
func (c *Client) Emails(ctx context.Context, token string) (string, error) {
	status, _, body, err := c.api(ctx, http.MethodGet, "/user/emails", token)
	if err != nil {
		return "", fmt.Errorf("emails: %w", err)
	}
	if err := apiStatus(status, true); err != nil { // 404: the user:email scope was not granted
		return "", fmt.Errorf("emails: %w", err)
	}
	var list []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if json.Unmarshal(body, &list) != nil {
		return "", fmt.Errorf("emails: %w: unusable body", ErrUnavailable)
	}
	for _, e := range list {
		if e.Primary && e.Verified && e.Email != "" {
			return e.Email, nil
		}
	}
	return "", nil
}

// Revoke deletes the access token at GitHub (never the app's grant).
func (c *Client) Revoke(ctx context.Context, token string) error {
	b, _ := json.Marshal(map[string]string{"access_token": token})
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		c.cfg.APIURL+"/applications/"+url.PathEscape(c.cfg.ClientID)+"/token", strings.NewReader(string(b)))
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.cfg.ClientID, string(c.cfg.ClientSecret))
	setAPIHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	status, _, _, err := c.do(req)
	if err != nil {
		return fmt.Errorf("revoke: %w", err)
	}
	// 404 is a token already invalid, which is the goal state.
	if status != http.StatusNoContent && status != http.StatusNotFound {
		return fmt.Errorf("revoke: %w: status %d", ErrUnavailable, status)
	}
	return nil
}

func setAPIHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", userAgent)
}

func (c *Client) api(ctx context.Context, method, path, token string) (int, http.Header, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.APIURL+path, nil)
	if err != nil {
		return 0, nil, nil, err
	}
	setAPIHeaders(req)
	req.Header.Set("Authorization", "Bearer "+token)
	return c.do(req)
}

// do sends req and reads at most maxBody bytes; transport errors and oversized bodies are ErrUnavailable.
func (c *Client) do(req *http.Request) (int, http.Header, []byte, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		return 0, nil, nil, fmt.Errorf("%w: unreadable or oversized body", ErrUnavailable)
	}
	return resp.StatusCode, resp.Header, body, nil
}

// apiStatus maps a REST status: 401 (and 404 when notFoundRejected) is the user's token, other non-2xx is GitHub's side.
func apiStatus(status int, notFoundRejected bool) error {
	switch {
	case status == http.StatusOK:
		return nil
	case status == http.StatusUnauthorized || (notFoundRejected && status == http.StatusNotFound):
		return fmt.Errorf("%w: status %d", ErrRejected, status)
	}
	return fmt.Errorf("%w: status %d", ErrUnavailable, status)
}

func splitScopes(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' })
}

// safe keeps an upstream error code short and printable before it goes into an error.
func safe(s string) string {
	if len(s) > 40 {
		s = s[:40]
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e {
			return '?'
		}
		return r
	}, s)
}
