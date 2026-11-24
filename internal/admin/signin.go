package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/kibanbuild/dojo/internal/github"
)

const (
	// Scope is the only scope the operator app asks for: read:user makes GET /user return
	// two_factor_authentication (design 3.7).
	Scope = "read:user"

	startPath    = "/auth/github/start"
	callbackPath = "/auth/github/callback"

	// oauthCookie is the pre-session cookie that keys the server-side OAuth state. It must be Lax:
	// GitHub's redirect back is a cross-site navigation.
	oauthCookie = "dojo_admin_oauth"
	oauthPath   = "/auth/github"
	stateTTL    = 5 * time.Minute
	// maxPending bounds the in-memory state table; past it a start is refused, never evicting a live flow.
	// A local client sending no Sec-Fetch-Site can fill it and block sign-in for up to the 5-minute TTL;
	// accepted, since only the loopback port-forward reaches the service.
	maxPending = 1000
)

// redirectURI is the callback registered with the operator OAuth App: loopback, the one Host served.
const redirectURI = "http://" + Host + callbackPath

// OAuth is the GitHub client the sign-in needs; *github.Client satisfies it.
type OAuth interface {
	AuthorizeURL(state, codeChallenge, redirectURI string) string
	Exchange(ctx context.Context, code, verifier, redirectURI string) (github.Token, error)
	User(ctx context.Context, token string) (github.User, error)
	Revoke(ctx context.Context, token string) error
}

// Deps are what the service needs besides its readiness probe. A zero GitHub or Store leaves the
// sign-in routes unmounted, so nothing can sign in.
type Deps struct {
	GitHub OAuth
	Store  Store
	// Learners serves the A2 lookup; nil leaves its routes unmounted.
	Learners LearnerStore
	Now      func() time.Time
	// Backoff is the wait before each GitHub revoke retry; nil means the default.
	Backoff []time.Duration
	// SecureCookies marks the cookies Secure. Off while the service is plain HTTP on loopback
	// (design 3.7); turn it on if it is ever served over TLS.
	SecureCookies bool
}

var defaultBackoff = []time.Duration{200 * time.Millisecond, 600 * time.Millisecond}

// signIn runs the operator OAuth flow (design 3.7).
type signIn struct {
	Deps
	pending pending
}

func (s *signIn) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Error codes shown on the sign-in page. GitHub's own text is never echoed.
var errorText = map[string]string{
	"state_check":        "The sign-in could not be verified. Start again.",
	"github_refused":     "GitHub refused the sign-in.",
	"github_unavailable": "GitHub could not be reached. Try again.",
	"not_authorized":     "This GitHub account cannot sign in here.",
	"two_factor":         "Turn on two-factor authentication on your GitHub account, then sign in again.",
	"server_error":       "Something went wrong. Try again.",
}

var signinPage = template.Must(template.New("signin").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Dojo operator sign-in</title>
<meta name="viewport" content="width=device-width, initial-scale=1"></head>
<body><main><h1>Operator sign-in</h1>
{{if .Error}}<p role="alert">{{.Error}}</p>{{end}}
<form method="post" action="` + startPath + `"><button type="submit">Sign in with GitHub</button></form>
</main></body></html>
`))

func (s *signIn) page(w http.ResponseWriter, r *http.Request) {
	render(w, http.StatusOK, signinPage, map[string]string{"Error": errorText[r.URL.Query().Get("error")]})
}

func toSignin(w http.ResponseWriter, r *http.Request, code string) {
	target := "/signin"
	if code != "" {
		target += "?error=" + code
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// start is a form POST. A browser that says the request came from another site is refused: a
// forged start would only let a page plant a pre-session cookie.
func (s *signIn) start(w http.ResponseWriter, r *http.Request) {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
	default:
		toSignin(w, r, "state_check")
		return
	}
	key, err1 := randToken()
	state, err2 := randToken()
	verifier, challenge, err3 := github.NewPKCE()
	if err1 != nil || err2 != nil || err3 != nil {
		slog.Error("operator sign-in start: no randomness")
		toSignin(w, r, "server_error")
		return
	}
	if !s.pending.put(hashHex(key), pendingState{state: state, verifier: verifier, expires: s.now().Add(stateTTL)}, s.now()) {
		slog.Warn("operator sign-in start: too many pending flows")
		toSignin(w, r, "server_error")
		return
	}
	http.SetCookie(w, s.oauthCookieFor(key, int(stateTTL.Seconds())))
	http.Redirect(w, r, s.GitHub.AuthorizeURL(state, challenge, redirectURI), http.StatusSeeOther)
}

func (s *signIn) oauthCookieFor(v string, maxAge int) *http.Cookie {
	return &http.Cookie{ //nolint:gosec // Secure follows Deps.SecureCookies by design (3.7)
		Name: oauthCookie, Value: v, Path: oauthPath, MaxAge: maxAge, HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: s.SecureCookies}
}

// callback finishes the flow. Every failure redirects to the sign-in page with a stable code.
func (s *signIn) callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var st pendingState
	ok := false
	if c, err := r.Cookie(oauthCookie); err == nil {
		st, ok = s.pending.take(hashHex(c.Value), s.now()) // one use: gone whatever follows
	}
	http.SetCookie(w, s.oauthCookieFor("", -1))
	if !ok || subtle.ConstantTimeCompare([]byte(st.state), []byte(q.Get("state"))) != 1 {
		toSignin(w, r, "state_check")
		return
	}
	if q.Get("error") != "" || q.Get("code") == "" {
		toSignin(w, r, "github_refused")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	u, err := s.identify(ctx, q.Get("code"), st.verifier)
	if err != nil {
		slog.Warn("operator sign-in: github step failed", "error", err)
		code := "github_unavailable"
		if errors.Is(err, github.ErrRejected) {
			code = "github_refused"
		}
		toSignin(w, r, code)
		return
	}
	op, err := s.Store.OperatorByGithubID(ctx, u.ID)
	switch {
	case errors.Is(err, ErrNotFound):
		slog.Warn("operator sign-in refused: not an active operator", "github_id", u.ID)
		toSignin(w, r, "not_authorized")
		return
	case err != nil:
		slog.Error("operator sign-in: operator lookup failed", "error", err)
		toSignin(w, r, "server_error")
		return
	}
	// Fail closed: only a granted read:user (or user) scope and a field that is exactly true pass.
	scoped := github.HasScope(u.Scopes, "read:user") || github.HasScope(u.Scopes, "user")
	if !scoped || u.TwoFactor == nil || !*u.TwoFactor {
		slog.Warn("operator sign-in refused: two-factor not confirmed", "operator", op.Login)
		toSignin(w, r, "two_factor")
		return
	}
	token, err := randToken()
	if err == nil {
		err = s.Store.CreateSession(ctx, op.ID, hashToken(token))
	}
	if err != nil {
		slog.Error("operator sign-in: session step failed", "error", err)
		toSignin(w, r, "server_error")
		return
	}
	http.SetCookie(w, s.sessionCookieFor(token, int(sessionTTL.Seconds())))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// identify exchanges the code, reads the account and always revokes the GitHub token. As in the
// public flow, a failed revoke fails the sign-in: no GitHub token outlives it.
func (s *signIn) identify(ctx context.Context, code, verifier string) (github.User, error) {
	tok, err := s.GitHub.Exchange(ctx, code, verifier, redirectURI)
	if err != nil {
		return github.User{}, err
	}
	u, err := s.GitHub.User(ctx, tok.AccessToken)
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	rerr := s.revoke(rctx, tok.AccessToken)
	if err != nil {
		if rerr != nil {
			slog.Warn("operator sign-in: revoke failed after an earlier error", "error", rerr)
		}
		return github.User{}, err
	}
	if rerr != nil {
		return github.User{}, rerr
	}
	return u, nil
}

func (s *signIn) revoke(ctx context.Context, token string) error {
	waits := s.Backoff
	if waits == nil {
		waits = defaultBackoff
	}
	err := s.GitHub.Revoke(ctx, token)
	for _, d := range waits {
		if err == nil {
			break
		}
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return err
		}
		err = s.GitHub.Revoke(ctx, token)
	}
	return err
}

type pendingState struct {
	state, verifier string
	expires         time.Time
}

// pending is the server-side OAuth state, in memory: the service runs as one task (desired count
// 1), and a restart only costs the operator a second click.
type pending struct {
	mu sync.Mutex
	m  map[string]pendingState
}

// put stores st under key and reports false when the table is full of live flows.
func (p *pending) put(key string, st pendingState, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.m == nil {
		p.m = map[string]pendingState{}
	}
	if len(p.m) >= maxPending {
		for k, v := range p.m {
			if !v.expires.After(now) {
				delete(p.m, k)
			}
		}
		if len(p.m) >= maxPending {
			return false
		}
	}
	p.m[key] = st
	return true
}

// take removes and returns the state under key, and reports false when it is absent or expired.
func (p *pending) take(key string, now time.Time) (pendingState, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	st, ok := p.m[key]
	delete(p.m, key)
	return st, ok && st.expires.After(now)
}

func randToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

func hashHex(token string) string { return hex.EncodeToString(hashToken(token)) }
