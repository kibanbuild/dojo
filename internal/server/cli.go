package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"regexp"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kibanbuild/dojo/internal/github"
	"github.com/kibanbuild/dojo/internal/server/repo"
)

const (
	// cliPath logs in (POST) and logs out (DELETE) the CLI (design 3.1).
	cliPath = "/v1/auth/cli"

	// A CLI token is cliTokenPrefix plus 32 random bytes in unpadded base64url: the prefix tells a
	// secret scanner and a human it is ours and apart from a session cookie, the body is as strong.
	cliTokenPrefix = "dojo_"
	cliTokenLen    = len(cliTokenPrefix) + tokenLen

	maxCLIBody       = 4 << 10
	maxDeviceNameLen = 64
)

var (
	githubTokenRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,255}$`)
	cliTokenRE    = regexp.MustCompile(`^dojo_[A-Za-z0-9_-]{43}$`)
	osRE          = regexp.MustCompile(`^[A-Za-z0-9._/+ -]{1,32}$`)
	cliVersionRE  = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,32}$`)
)

func validCLIToken(s string) bool { return len(s) == cliTokenLen && cliTokenRE.MatchString(s) }

// CLIGitHub is the GitHub client the CLI login needs.
type CLIGitHub interface {
	CheckToken(ctx context.Context, token string) (github.TokenInfo, error)
	Emails(ctx context.Context, token string) (string, error)
	Revoke(ctx context.Context, token string) error
}

// CLIStore is the data the CLI login needs.
type CLIStore interface {
	GetLearnerByGithubID(ctx context.Context, githubID int64) (repo.Learner, error)
	UpsertLearner(ctx context.Context, arg repo.UpsertLearnerParams) (repo.Learner, error)
}

// CLITokens writes a new CLI token and announces the login on the learner's stream in one transaction.
type CLITokens interface {
	InsertCLITokenAndNotify(ctx context.Context, arg repo.InsertCLITokenParams) (repo.InsertCLITokenRow, error)
}

// CLILogin turns a GitHub device-flow token into a CLI token (design 3.1).
type CLILogin struct {
	GitHub CLIGitHub
	Store  CLIStore
	Tokens CLITokens
	// Backoff is the wait before each GitHub revoke retry; nil means the default.
	Backoff []time.Duration
}

type cliLoginRequest struct {
	AccessToken string `json:"access_token"`
	DeviceName  string `json:"device_name"`
	OS          string `json:"os"`
	CLIVersion  string `json:"cli_version"`
}

// valid reports whether every field is within its limits.
func (q cliLoginRequest) valid() bool {
	if !githubTokenRE.MatchString(q.AccessToken) || !osRE.MatchString(q.OS) || !cliVersionRE.MatchString(q.CLIVersion) {
		return false
	}
	n := utf8.RuneCountInString(q.DeviceName)
	if n < 1 || n > maxDeviceNameLen || !utf8.ValidString(q.DeviceName) {
		return false
	}
	for _, r := range q.DeviceName {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == unicode.ReplacementChar {
			return false
		}
	}
	return q.DeviceName[0] != ' ' && q.DeviceName[len(q.DeviceName)-1] != ' '
}

type cliLoginResponse struct {
	Token string `json:"token"`
}

// login is POST /v1/auth/cli. It is unauthenticated (the credential is the GitHub token in the
// body), so Session ignores any cookie or Authorization header here and CSRF does not apply.
func (c *CLILogin) login(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxCLIBody)
	var req cliLoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !req.valid() {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	info, email, err := c.identify(ctx, req.AccessToken)
	if err != nil {
		slog.Warn("cli login: github step failed", "error", err)
		if errors.Is(err, github.ErrRejected) {
			writeError(w, http.StatusUnauthorized, "github_rejected")
			return
		}
		writeError(w, http.StatusBadGateway, "github_unavailable")
		return
	}
	l, err := c.learner(ctx, info, email)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, http.StatusForbidden, "signup_required")
		return
	case err != nil:
		slog.Error("cli login: learner step failed", "error", err)
		writeError(w, http.StatusInternalServerError, "server_error")
		return
	case l.SuspendedAt.Valid:
		writeError(w, http.StatusForbidden, "account_suspended")
		return
	}
	token, err := newCLIToken()
	if err == nil {
		_, err = c.Tokens.InsertCLITokenAndNotify(ctx, repo.InsertCLITokenParams{OrgID: l.OrgID, LearnerID: l.ID, TokenHash: HashToken(token),
			DeviceName: req.DeviceName, Os: req.OS, CliVersion: req.CLIVersion})
	}
	if err != nil {
		slog.Error("cli login: token step failed", "error", err)
		writeError(w, http.StatusInternalServerError, "server_error")
		return
	}
	writeJSON(w, http.StatusCreated, cliLoginResponse{Token: token})
}

// identify checks the token is ours, reads the account and always revokes the GitHub token. As in
// the web flow, a failed revoke fails the login (the CLI retries it from the device flow): no GitHub
// token outlives it, and none is stored or logged.
func (c *CLILogin) identify(ctx context.Context, token string) (github.TokenInfo, string, error) {
	info, err := c.GitHub.CheckToken(ctx, token)
	if err != nil {
		// Nothing proved the token is ours, so there is nothing of ours to revoke.
		return github.TokenInfo{}, "", err
	}
	email := ""
	// A grant without user:email still logs in, with no email.
	if github.HasScope(info.Scopes, "user:email") {
		email, err = c.GitHub.Emails(ctx, token)
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	rerr := revokeWithRetry(rctx, c.GitHub.Revoke, token, c.Backoff)
	if err != nil {
		if rerr != nil {
			slog.Warn("cli login: revoke failed after an earlier error", "error", rerr)
		}
		return github.TokenInfo{}, "", err
	}
	if rerr != nil {
		return github.TokenInfo{}, "", rerr
	}
	return info, email, nil
}

// learner returns the existing learner for the GitHub account, refreshed like a web sign-in
// (login and email; a suspended learner is left untouched). An unknown account is pgx.ErrNoRows:
// age and terms are confirmed only on O1, so the CLI cannot create learners.
func (c *CLILogin) learner(ctx context.Context, info github.TokenInfo, email string) (repo.Learner, error) {
	known, err := c.Store.GetLearnerByGithubID(ctx, info.UserID)
	if err != nil || known.SuspendedAt.Valid {
		return known, err
	}
	return c.Store.UpsertLearner(ctx, repo.UpsertLearnerParams{OrgID: known.OrgID, GithubID: info.UserID, GithubLogin: info.Login,
		Username: known.Username, Email: pgtype.Text{String: email, Valid: email != ""}, Language: known.Language,
		AgeConfirmedAt: known.AgeConfirmedAt, TermsAcceptedAt: known.TermsAcceptedAt, TermsVersion: known.TermsVersion,
		UsageStats: known.UsageStats})
}

func newCLIToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return cliTokenPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}
