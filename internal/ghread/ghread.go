// Package ghread is the CLI's unauthenticated, read-only GitHub client for the doctor's GitHub
// checks (design 3.9). It never sends a token, bounds every dial and request, makes at most a fixed
// number of calls per run, and reports anything it cannot tell as ErrUnknown, never as a failure.
package ghread

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultAPIURL is the real GitHub, used when DOJO_CLI_GITHUB_API_URL is unset.
	DefaultAPIURL = "https://api.github.com"
	// DefaultBudget is the number of calls one run may make (design 3.9).
	DefaultBudget  = 7
	defaultTimeout = 5 * time.Second
	maxBody        = 1 << 20
	maxRedirects   = 2
)

var (
	// ErrNotFound is a 404: a missing or private repo, an unknown login or an unknown path.
	ErrNotFound = errors.New("github: not found")
	// ErrUnknown marks an outcome the CLI cannot judge: rate limit, network failure, a 5xx, an
	// unexpected answer or an exhausted call budget. Check it with errors.Is; an *UnknownError carries the reason.
	ErrUnknown = errors.New("github: unknown")
	// ErrInvalid is a login, repo, branch or path that was refused before any request was made.
	ErrInvalid = errors.New("github: invalid argument")
)

// UnknownError explains an unknown outcome. RetryAfter is set for a rate limit.
type UnknownError struct {
	Reason     string
	RetryAfter time.Duration
}

func (e *UnknownError) Error() string { return "github: " + e.Reason }

// Is makes errors.Is(err, ErrUnknown) true.
func (e *UnknownError) Is(target error) bool { return target == ErrUnknown }

// Config configures a Client. Zero values take the defaults.
type Config struct {
	BaseURL string
	Timeout time.Duration
	Budget  int
	Now     func() time.Time
}

// LoadConfig reads DOJO_CLI_GITHUB_API_URL. The value must be a bare origin: https, or http for a
// loopback host (the local stack's fake GitHub).
func LoadConfig(get func(string) string) (Config, error) {
	v := strings.TrimRight(get("DOJO_CLI_GITHUB_API_URL"), "/")
	if v == "" {
		return Config{BaseURL: DefaultAPIURL}, nil
	}
	if !bareOrigin(v) {
		return Config{}, errors.New("DOJO_CLI_GITHUB_API_URL must be a bare origin (https, or http on a loopback host)")
	}
	return Config{BaseURL: v}, nil
}

func bareOrigin(v string) bool {
	u, err := url.Parse(v)
	if err != nil || strings.ContainsAny(v, "?#") || u.User != nil || u.ForceQuery || u.Hostname() == "" ||
		strings.HasSuffix(u.Host, ":") || u.Path != "" {
		return false
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return false
		}
	}
	if u.Scheme == "http" {
		h := u.Hostname()
		ip := net.ParseIP(h)
		return h == "localhost" || (ip != nil && ip.IsLoopback())
	}
	return u.Scheme == "https"
}

// Client makes the reads of one doctor run. Create a new one per run: it holds the call budget.
type Client struct {
	base *url.URL
	hc   *http.Client
	now  func() time.Time

	mu     sync.Mutex
	budget int
}

// New builds a Client from cfg.
func New(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultAPIURL
	}
	if !bareOrigin(cfg.BaseURL) {
		return nil, errors.New("ghread: BaseURL must be a bare origin")
	}
	base, _ := url.Parse(cfg.BaseURL)
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.Budget <= 0 {
		cfg.Budget = DefaultBudget
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	tr := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: cfg.Timeout}).DialContext,
		TLSHandshakeTimeout:   cfg.Timeout,
		ResponseHeaderTimeout: cfg.Timeout,
		MaxIdleConns:          2,
		IdleConnTimeout:       cfg.Timeout,
	}
	c := &Client{base: base, now: cfg.Now, budget: cfg.Budget}
	c.hc = &http.Client{Transport: tr, Timeout: cfg.Timeout, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		// Only the configured host, never a downgrade, and each hop costs a call: anything else is read as the 3xx it is.
		if len(via) > maxRedirects || req.URL.Scheme != base.Scheme || req.URL.Host != base.Host || !c.spend() {
			return http.ErrUseLastResponse
		}
		return nil
	}}
	return c, nil
}

// spend takes one call from the budget and reports whether there was one.
func (c *Client) spend() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.budget <= 0 {
		return false
	}
	c.budget--
	return true
}

var (
	loginRE = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	repoRE  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
)

func validLogin(s string) bool { return loginRE.MatchString(s) }

func validRepo(s string) bool { return repoRE.MatchString(s) && s != "." && s != ".." }

// validPath accepts relative file paths of safe segments, with no "." or ".." segment.
func validPath(s string) bool {
	if s == "" || len(s) > 255 {
		return false
	}
	for _, seg := range strings.Split(s, "/") {
		if !validRepo(seg) {
			return false
		}
	}
	return true
}

func invalid(what, v string) error { return fmt.Errorf("%w: %s %q", ErrInvalid, what, v) }

// get spends one call from the budget and decodes a 200 answer into out.
func (c *Client) get(ctx context.Context, path string, out any) error {
	if !c.spend() {
		return &UnknownError{Reason: "call budget for this run is spent"}
	}

	u := *c.base
	u.Path = path
	u.RawPath = ""
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return &UnknownError{Reason: "bad request"}
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "dojo-cli")
	resp, err := c.hc.Do(req)
	if err != nil {
		return &UnknownError{Reason: "GitHub is unreachable"}
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusOK:
		if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(out); err != nil {
			return &UnknownError{Reason: "GitHub sent an answer the CLI could not read"}
		}
		return nil
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests) &&
		resp.Header.Get("X-RateLimit-Remaining") == "0":
		return c.rateLimited(resp)
	default:
		return &UnknownError{Reason: fmt.Sprintf("GitHub answered %d", resp.StatusCode)}
	}
}

func (c *Client) rateLimited(resp *http.Response) error {
	wait := time.Duration(0)
	if reset, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
		wait = max(time.Unix(reset, 0).Sub(c.now()), 0)
	}
	mins := int((wait + time.Minute - 1) / time.Minute)
	msg := "GitHub is limiting requests, try again later"
	if mins == 1 {
		msg = "GitHub is limiting requests, try again in 1 minute"
	} else if mins > 1 {
		msg = fmt.Sprintf("GitHub is limiting requests, try again in %d minutes", mins)
	}
	return &UnknownError{Reason: msg, RetryAfter: wait}
}

// User is the public account answer of GET /user/{id}.
type User struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
}

// Key is an SSH key of an account, authentication or signing.
type Key struct {
	ID  int64  `json:"id"`
	Key string `json:"key"`
}

// Repo is the part of GET /repos/{owner}/{repo} the checks read.
type Repo struct {
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"default_branch"`
	HTMLURL       string `json:"html_url"`
}

// Rule is one rule active on a branch.
type Rule struct {
	Type      string `json:"type"`
	RulesetID int64  `json:"ruleset_id"`
}

// Content is the metadata of a file in a repo.
type Content struct {
	Type string `json:"type"`
	Name string `json:"name"`
	Path string `json:"path"`
	SHA  string `json:"sha"`
}

// UserByID is GET /user/{id}.
func (c *Client) UserByID(ctx context.Context, id int64) (User, error) {
	var u User
	if id <= 0 {
		return u, invalid("id", strconv.FormatInt(id, 10))
	}
	err := c.get(ctx, "/user/"+strconv.FormatInt(id, 10), &u)
	return u, err
}

// Keys is GET /users/{login}/keys: the account's authentication keys.
func (c *Client) Keys(ctx context.Context, login string) ([]Key, error) {
	return keys(ctx, c, login, "keys")
}

// SigningKeys is GET /users/{login}/ssh_signing_keys.
func (c *Client) SigningKeys(ctx context.Context, login string) ([]Key, error) {
	return keys(ctx, c, login, "ssh_signing_keys")
}

func keys(ctx context.Context, c *Client, login, kind string) ([]Key, error) {
	if !validLogin(login) {
		return nil, invalid("login", login)
	}
	var out []Key
	err := c.get(ctx, "/users/"+login+"/"+kind, &out)
	return out, err
}

// Repo is GET /repos/{owner}/{repo}.
func (c *Client) Repo(ctx context.Context, owner, repo string) (Repo, error) {
	var r Repo
	if !validLogin(owner) {
		return r, invalid("owner", owner)
	}
	if !validRepo(repo) {
		return r, invalid("repo", repo)
	}
	err := c.get(ctx, "/repos/"+owner+"/"+repo, &r)
	return r, err
}

// BranchRules is GET /repos/{owner}/{repo}/rules/branches/{branch}.
func (c *Client) BranchRules(ctx context.Context, owner, repo, branch string) ([]Rule, error) {
	if !validLogin(owner) {
		return nil, invalid("owner", owner)
	}
	if !validRepo(repo) {
		return nil, invalid("repo", repo)
	}
	if len(branch) > 100 || !validPath(branch) {
		return nil, invalid("branch", branch)
	}
	var out []Rule
	err := c.get(ctx, "/repos/"+owner+"/"+repo+"/rules/branches/"+branch, &out)
	return out, err
}

// Contents is GET /repos/{owner}/{repo}/contents/{path}, for a file.
func (c *Client) Contents(ctx context.Context, owner, repo, path string) (Content, error) {
	var out Content
	if !validLogin(owner) {
		return out, invalid("owner", owner)
	}
	if !validRepo(repo) {
		return out, invalid("repo", repo)
	}
	if !validPath(path) {
		return out, invalid("path", path)
	}
	err := c.get(ctx, "/repos/"+owner+"/"+repo+"/contents/"+path, &out)
	return out, err
}
