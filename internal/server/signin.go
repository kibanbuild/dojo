package server

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kibanbuild/dojo/internal/github"
	"github.com/kibanbuild/dojo/internal/server/repo"
)

const (
	startPath    = "/v1/auth/github/start"
	callbackPath = "/v1/auth/github/callback"

	stateCookie  = "dojo_oauth"
	statePath    = "/v1/auth/github"
	stateTTL     = 10 * time.Minute
	stateSkew    = time.Minute
	stateVersion = 1
	macLabel     = "dojo-oauth-state-v1."
	// minKeyLen is the shortest accepted AUTH_STATE_KEY, in bytes.
	minKeyLen = 32
	// DevStateKey is the public local value: ParseStateKey refuses it outside local.
	DevStateKey = "local-dev-state-key-not-a-secret-0123456789"

	publicOrg = "public"
)

// OAuth is the GitHub client the sign-in flow needs.
type OAuth interface {
	AuthorizeURL(state, codeChallenge, redirectURI string) string
	Exchange(ctx context.Context, code, verifier, redirectURI string) (github.Token, error)
	User(ctx context.Context, token string) (github.User, error)
	Emails(ctx context.Context, token string) (string, error)
	Revoke(ctx context.Context, token string) error
}

// SignInStore is the data the web sign-in needs.
type SignInStore interface {
	GetOrgBySlug(ctx context.Context, slug string) (repo.Org, error)
	GetLearnerByGithubID(ctx context.Context, githubID int64) (repo.Learner, error)
	UpsertLearner(ctx context.Context, arg repo.UpsertLearnerParams) (repo.Learner, error)
	// CreateLearner inserts a new learner and queues their welcome mail (E1) in the same statement.
	CreateLearner(ctx context.Context, arg repo.UpsertLearnerParams) (repo.Learner, error)
	SetLearnerAnalyticsConsentIfUnset(ctx context.Context, arg repo.SetLearnerAnalyticsConsentIfUnsetParams) (int64, error)
	InsertSession(ctx context.Context, arg repo.InsertSessionParams) (repo.InsertSessionRow, error)
	RevokeSession(ctx context.Context, id pgtype.UUID) error
}

// SignIn runs the web OAuth flow (design 3.1).
type SignIn struct {
	GitHub OAuth
	Store  SignInStore
	Auth   Auth   // for the session cookie
	Origin string // PUBLIC_ORIGIN without a trailing slash
	Key    []byte // signs the state cookie
	// TermsVersion is the terms version stored on a new learner, from TERMS_VERSION.
	TermsVersion string
	Now          func() time.Time
	// Backoff is the wait before each GitHub revoke retry; nil means the default.
	Backoff []time.Duration

	limits *RateLimits // set by New
	spent  spentStates
}

// maxSpent bounds the spent-state table; at the cap the entry expiring first is evicted.
const maxSpent = 50_000

// spentStates makes a state single-use until its cookie expires. It is per task: the per-IP callback
// limiter is the bound across tasks.
type spentStates struct {
	mu sync.Mutex
	m  map[string]time.Time // state -> expiry
}

// take records state and reports whether this is its first use.
func (s *spentStates) take(state string, now, expiry time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]time.Time{}
	}
	if _, used := s.m[state]; used {
		return false
	}
	if len(s.m) >= maxSpent {
		for k, e := range s.m {
			if !e.After(now) {
				delete(s.m, k)
			}
		}
		if len(s.m) >= maxSpent {
			// Never refuse for a full table: an evicted state can be replayed once more, and the per-IP callback limiter bounds that.
			oldest, first := "", true
			var oldestExp time.Time
			for k, e := range s.m {
				if first || e.Before(oldestExp) {
					oldest, oldestExp, first = k, e, false
				}
			}
			delete(s.m, oldest)
		}
	}
	s.m[state] = expiry
	return true
}

// allowCallback takes a token from the per-client callback limiter, when one is configured.
func (s *SignIn) allowCallback(r *http.Request) bool {
	if s.limits == nil || s.limits.Callback == nil {
		return true
	}
	ok, _ := s.limits.Callback.Allow(clientKey(r, s.limits.Hops))
	return ok
}

var defaultBackoff = []time.Duration{200 * time.Millisecond, 600 * time.Millisecond}

// secureStatePrefix pins the cookie to https origins. Sibling subdomains can still set it: a known residual.
const secureStatePrefix = "__Secure-"

func (s *SignIn) stateName() string {
	if s.Auth.Secure {
		return secureStatePrefix + stateCookie
	}
	return stateCookie
}

// crossSite reports a start request that a browser says came from another site.
func (s *SignIn) crossSite(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
	default:
		return true
	}
	o := r.Header.Get("Origin")
	return o != "" && o != s.Origin
}

var termsVersionRE = regexp.MustCompile(`^[0-9a-z.-]{1,32}$`)

// ParseTermsVersion validates TERMS_VERSION: required, 1 to 32 of lowercase letters, digits, dots and hyphens.
func ParseTermsVersion(v string) (string, error) {
	if !termsVersionRE.MatchString(v) {
		return "", fmt.Errorf("TERMS_VERSION must match %s, got %q", termsVersionRE, v)
	}
	// Reserved: migration 00004 backfills it on rows created before the column existed.
	if v == "unrecorded" {
		return "", fmt.Errorf("TERMS_VERSION %q is reserved for rows with no recorded version", v)
	}
	return v, nil
}

// ParseStateKey validates AUTH_STATE_KEY: at least 32 bytes, with some variety, and never the dev value outside local.
func ParseStateKey(key string, local bool) ([]byte, error) {
	seen := map[rune]bool{}
	for _, r := range key {
		seen[r] = true
	}
	switch {
	case len(key) < minKeyLen:
		return nil, fmt.Errorf("AUTH_STATE_KEY must be at least %d bytes", minKeyLen)
	case len(seen) < 8:
		return nil, errors.New("AUTH_STATE_KEY is too repetitive")
	case key == DevStateKey && !local:
		return nil, errors.New("AUTH_STATE_KEY is the public local value")
	}
	return []byte(key), nil
}

type oauthState struct {
	V        int    `json:"v"`
	State    string `json:"s"`
	Verifier string `json:"c"`
	Age      bool   `json:"a"`
	Terms    bool   `json:"t"`
	Issued   int64  `json:"i"`
}

func (s *SignIn) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *SignIn) mac(payload string) []byte {
	m := hmac.New(sha256.New, s.Key)
	m.Write([]byte(macLabel + payload))
	return m.Sum(nil)
}

// seal is base64url(json) "." base64url(hmac): signed, not encrypted; the verifier is only useful with the code.
func (s *SignIn) seal(st oauthState) string {
	b, _ := json.Marshal(st)
	p := base64.RawURLEncoding.EncodeToString(b)
	return p + "." + base64.RawURLEncoding.EncodeToString(s.mac(p))
}

func (s *SignIn) open(v string) (oauthState, bool) {
	p, sig, ok := strings.Cut(v, ".")
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if !ok || err != nil || !hmac.Equal(got, s.mac(p)) {
		return oauthState{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(p)
	var st oauthState
	if err != nil || json.Unmarshal(raw, &st) != nil || st.V != stateVersion || st.State == "" || st.Verifier == "" {
		return oauthState{}, false
	}
	age := s.now().Sub(time.Unix(st.Issued, 0))
	return st, age <= stateTTL && age >= -stateSkew
}

func (s *SignIn) stateCookie(v string, maxAge int) *http.Cookie {
	return &http.Cookie{ //nolint:gosec // Secure follows PUBLIC_ORIGIN by design (3.1)
		Name: s.stateName(), Value: v, Path: statePath, MaxAge: maxAge,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.Auth.Secure,
	}
}

func (s *SignIn) redirectURI() string { return s.Origin + callbackPath }

func randToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func ticked(v string) bool { return v == "on" || v == "true" }

// start is a browser form POST. It is unauthenticated, so the cookie CSRF rule does not apply (CSRF
// exempts this path); a forged start is harmless because the callback only accepts the state in
// the browser's own cookie. The ticks come from the body only.
func (s *SignIn) start(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	// A cross-site start could stamp the ticks for a victim GitHub redirects silently.
	if s.crossSite(r) {
		redirectSignin(w, r, "error=state_check")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	state, err := randToken()
	verifier, challenge, err2 := github.NewPKCE()
	if err != nil || err2 != nil {
		slog.Error("sign-in start: no randomness")
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	st := oauthState{V: stateVersion, State: state, Verifier: verifier, Issued: s.now().Unix(),
		Age: ticked(r.PostForm.Get("age")), Terms: ticked(r.PostForm.Get("terms"))}
	http.SetCookie(w, s.stateCookie(s.seal(st), int(stateTTL.Seconds())))
	http.Redirect(w, r, s.GitHub.AuthorizeURL(state, challenge, s.redirectURI()), http.StatusSeeOther)
}

func redirectSignin(w http.ResponseWriter, r *http.Request, query string) {
	http.Redirect(w, r, "/app/signin?"+query, http.StatusSeeOther)
}

// callback finishes the flow. Every failure redirects to O1 with a stable code; GitHub's text is never echoed.
func (s *SignIn) callback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	q := r.URL.Query()
	var st oauthState
	ok := false
	if c, err := r.Cookie(s.stateName()); err == nil {
		st, ok = s.open(c.Value)
	}
	http.SetCookie(w, s.stateCookie("", -1))
	if !ok || subtle.ConstantTimeCompare([]byte(st.State), []byte(q.Get("state"))) != 1 {
		redirectSignin(w, r, "error=state_check")
		return
	}
	// One use per state, then the callback limiter: both before any call to GitHub.
	expiry := time.Unix(st.Issued, 0).Add(stateTTL + stateSkew)
	if !s.spent.take(st.State, s.now(), expiry) {
		redirectSignin(w, r, "error=state_check")
		return
	}
	if !s.allowCallback(r) {
		redirectSignin(w, r, "error=rate_limited")
		return
	}
	switch e := q.Get("error"); {
	case e == "access_denied":
		redirectSignin(w, r, "error=access_denied")
		return
	case e != "" || q.Get("code") == "":
		redirectSignin(w, r, "error=github_refused")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	u, email, err := s.identify(ctx, q.Get("code"), st.Verifier)
	if err != nil {
		slog.Warn("sign-in: github step failed", "error", err)
		code := "github_unavailable"
		if errors.Is(err, github.ErrRejected) {
			code = "github_refused"
		}
		redirectSignin(w, r, "error="+code)
		return
	}
	l, err := s.learner(ctx, u, email, st)
	switch {
	case errors.Is(err, errNeedsConfirm):
		redirectSignin(w, r, "confirm=1")
		return
	case err != nil:
		slog.Error("sign-in: learner step failed", "error", err)
		redirectSignin(w, r, "error=server_error")
		return
	case l.SuspendedAt.Valid:
		redirectSignin(w, r, "error=suspended")
		return
	}
	s.copyAnalyticsConsent(ctx, r, l)
	token, err := randToken()
	if err == nil {
		_, err = s.Store.InsertSession(ctx, repo.InsertSessionParams{OrgID: l.OrgID, LearnerID: l.ID, TokenHash: HashToken(token)})
	}
	if err != nil {
		slog.Error("sign-in: session step failed", "error", err)
		redirectSignin(w, r, "error=server_error")
		return
	}
	// The browser changes identity: the session it presented, if any, ends.
	if p, ok := principalOf(r.Context()); ok && !p.bearer {
		if err := s.Store.RevokeSession(ctx, p.session); err != nil {
			slog.Warn("sign-in: revoking the previous session failed", "error", err)
		}
	}
	http.SetCookie(w, s.Auth.SetCookie(token))
	http.Redirect(w, r, "/app", http.StatusSeeOther)
}

// consentCookie is the signed-out visitor's analytics choice, set by the web app.
const consentCookie = "dojo_analytics_consent"

// copyAnalyticsConsent copies the consent cookie onto the account while the account has no choice. Only the
// exact values granted and denied count; the update is conditional on NULL so a concurrent PATCH is never
// overwritten. A failure is logged and never fails the sign-in.
func (s *SignIn) copyAnalyticsConsent(ctx context.Context, r *http.Request, l repo.Learner) {
	c, err := r.Cookie(consentCookie)
	if err != nil || (c.Value != "granted" && c.Value != "denied") {
		return
	}
	_, err = s.Store.SetLearnerAnalyticsConsentIfUnset(ctx, repo.SetLearnerAnalyticsConsentIfUnsetParams{
		ID: l.ID, AnalyticsConsent: pgtype.Bool{Bool: c.Value == "granted", Valid: true}})
	if err != nil {
		slog.Warn("sign-in: copying the analytics consent failed", "error", err)
	}
}

// identify exchanges the code, reads the account and always revokes the GitHub token. As in the CLI
// flow, a failed revoke fails the sign-in (the learner retries): no GitHub token outlives it.
func (s *SignIn) identify(ctx context.Context, code, verifier string) (github.User, string, error) {
	tok, err := s.GitHub.Exchange(ctx, code, verifier, s.redirectURI())
	if err != nil {
		return github.User{}, "", err
	}
	u, err := s.GitHub.User(ctx, tok.AccessToken)
	email := ""
	// A grant without user:email still signs in, with no email.
	if err == nil && github.HasScope(tok.Scopes, "user:email") {
		email, err = s.GitHub.Emails(ctx, tok.AccessToken)
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	rerr := s.revoke(rctx, tok.AccessToken)
	if err != nil {
		if rerr != nil {
			slog.Warn("sign-in: revoke failed after an earlier error", "error", rerr)
		}
		return github.User{}, "", err
	}
	if rerr != nil {
		return github.User{}, "", rerr
	}
	return u, email, nil
}

// revoke retries after each Backoff wait, within ctx's bound.
func (s *SignIn) revoke(ctx context.Context, token string) error {
	return revokeWithRetry(ctx, s.GitHub.Revoke, token, s.Backoff)
}

// revokeWithRetry calls revoke, then again after each wait of backoff (nil means the default).
func revokeWithRetry(ctx context.Context, revoke func(context.Context, string) error, token string, backoff []time.Duration) error {
	waits := backoff
	if waits == nil {
		waits = defaultBackoff
	}
	err := revoke(ctx, token)
	for _, d := range waits {
		if err == nil {
			break
		}
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return err
		}
		err = revoke(ctx, token)
	}
	return err
}

var errNeedsConfirm = errors.New("age and terms not confirmed")

// learner returns the learner for the GitHub account: refreshed when known, created when both ticks are set.
func (s *SignIn) learner(ctx context.Context, u github.User, email string, st oauthState) (repo.Learner, error) {
	mail := pgtype.Text{String: email, Valid: email != ""}
	known, err := s.Store.GetLearnerByGithubID(ctx, u.ID)
	switch {
	case err == nil:
		if known.SuspendedAt.Valid {
			return known, nil
		}
		return s.Store.UpsertLearner(ctx, repo.UpsertLearnerParams{OrgID: known.OrgID, GithubID: u.ID, GithubLogin: u.Login,
			Username: known.Username, Email: mail, Language: known.Language, AgeConfirmedAt: known.AgeConfirmedAt,
			TermsAcceptedAt: known.TermsAcceptedAt, TermsVersion: known.TermsVersion, UsageStats: known.UsageStats})
	case !errors.Is(err, pgx.ErrNoRows):
		return repo.Learner{}, err
	case !st.Age || !st.Terms:
		return repo.Learner{}, errNeedsConfirm
	}
	org, err := s.Store.GetOrgBySlug(ctx, publicOrg)
	if err != nil {
		return repo.Learner{}, err
	}
	// The server clock records the confirmation; the cookie only said the boxes were ticked.
	at := pgtype.Timestamptz{Time: s.now(), Valid: true}
	return createWithUsername(u.Login, u.ID, func(name string) (repo.Learner, error) {
		return s.Store.CreateLearner(ctx, repo.UpsertLearnerParams{OrgID: org.ID, GithubID: u.ID, GithubLogin: u.Login,
			Username: name, Email: mail, Language: "en", AgeConfirmedAt: at, TermsAcceptedAt: at, TermsVersion: s.TermsVersion, UsageStats: true})
	})
}
