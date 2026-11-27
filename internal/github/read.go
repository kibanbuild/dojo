package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
)

var (
	// ErrNotFound is a 404 on a public read: the account, repository or commit is not there.
	ErrNotFound = errors.New("github: not found")
	// ErrRateLimited is a 403 or 429 that says the rate limit is spent; it is GitHub's side, never the learner's.
	ErrRateLimited = errors.New("github: rate limited")

	loginRE  = regexp.MustCompile(`^[A-Za-z0-9-]{1,39}$`)
	shaRE    = regexp.MustCompile(`^[0-9a-f]{40}$`)
	branchRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
)

// CommitInfo is what the verifier reads of a commit.
type CommitInfo struct {
	SHA            string
	Message        string
	Verified       bool
	VerifiedReason string
}

// Comparison is the answer of compare/{base}...{head}: Status is ahead, behind, identical or diverged.
type Comparison struct {
	Status            string
	AheadBy, BehindBy int
}

// UserByID reads the public account with the stable numeric id, so a renamed login can be found again.
func (c *Client) UserByID(ctx context.Context, id int64) (User, error) {
	if id < 1 {
		return User{}, errors.New("user by id: invalid id")
	}
	body, err := c.publicGet(ctx, "/user/"+strconv.FormatInt(id, 10))
	if err != nil {
		return User{}, fmt.Errorf("user by id: %w", err)
	}
	var out struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}
	if json.Unmarshal(body, &out) != nil || out.ID != id || !loginRE.MatchString(out.Login) {
		return User{}, fmt.Errorf("user by id: %w: unusable body", ErrUnavailable)
	}
	return User{ID: out.ID, Login: out.Login}, nil
}

// Commit reads one commit of the public repository login/dojo.
func (c *Client) Commit(ctx context.Context, login, sha string) (CommitInfo, error) {
	if !loginRE.MatchString(login) || !shaRE.MatchString(sha) {
		return CommitInfo{}, errors.New("commit: invalid login or sha")
	}
	body, err := c.publicGet(ctx, "/repos/"+login+"/dojo/commits/"+sha)
	if err != nil {
		return CommitInfo{}, fmt.Errorf("commit: %w", err)
	}
	var out struct {
		SHA    string `json:"sha"`
		Commit struct {
			Message      string `json:"message"`
			Verification struct {
				Verified bool   `json:"verified"`
				Reason   string `json:"reason"`
			} `json:"verification"`
		} `json:"commit"`
	}
	if json.Unmarshal(body, &out) != nil || out.SHA != sha {
		return CommitInfo{}, fmt.Errorf("commit: %w: unusable body", ErrUnavailable)
	}
	v := out.Commit.Verification
	return CommitInfo{SHA: out.SHA, Message: out.Commit.Message, Verified: v.Verified, VerifiedReason: v.Reason}, nil
}

// Compare reads compare/{base}...{head} of login/dojo; base is a branch name and head a sha.
func (c *Client) Compare(ctx context.Context, login, base, head string) (Comparison, error) {
	if !loginRE.MatchString(login) || !branchRE.MatchString(base) || strings.Contains(base, "..") || !shaRE.MatchString(head) {
		return Comparison{}, errors.New("compare: invalid login, base or head")
	}
	body, err := c.publicGet(ctx, "/repos/"+login+"/dojo/compare/"+base+"..."+head)
	if err != nil {
		return Comparison{}, fmt.Errorf("compare: %w", err)
	}
	var out struct {
		Status   string `json:"status"`
		AheadBy  int    `json:"ahead_by"`
		BehindBy int    `json:"behind_by"`
	}
	if json.Unmarshal(body, &out) != nil || out.Status == "" {
		return Comparison{}, fmt.Errorf("compare: %w: unusable body", ErrUnavailable)
	}
	return Comparison{Status: out.Status, AheadBy: out.AheadBy, BehindBy: out.BehindBy}, nil
}

// publicGet is a GET of a public path under the configured origin. The URL is checked to stay on that
// origin before the read token is attached, and redirects are never followed.
func (c *Client) publicGet(ctx context.Context, p string) ([]byte, error) {
	base, err := url.Parse(c.cfg.APIURL)
	if err != nil || base.Host == "" {
		return nil, fmt.Errorf("%w: bad API origin", ErrUnavailable)
	}
	u, err := url.Parse(c.cfg.APIURL + p)
	if err != nil || u.Scheme != base.Scheme || u.Host != base.Host || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || !strings.HasPrefix(p, "/") || path.Clean(p) != p {
		return nil, errors.New("path leaves the GitHub origin")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	setAPIHeaders(req)
	if c.cfg.ReadToken != "" {
		req.Header.Set("Authorization", "Bearer "+string(c.cfg.ReadToken))
	}
	status, h, body, err := c.do(req)
	if err != nil {
		return nil, err
	}
	switch {
	case status == http.StatusOK:
		return body, nil
	case status == http.StatusNotFound:
		return nil, ErrNotFound
	case status == http.StatusTooManyRequests,
		status == http.StatusForbidden && (h.Get("X-RateLimit-Remaining") == "0" || h.Get("Retry-After") != ""):
		return nil, ErrRateLimited
	}
	return nil, fmt.Errorf("%w: status %d", ErrUnavailable, status)
}
