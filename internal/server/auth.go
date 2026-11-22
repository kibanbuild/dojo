package server

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kibanbuild/dojo/internal/server/repo"
)

const (
	// SessionCookie is the session cookie name (design 3.1).
	SessionCookie = "dojo_session"
	// CSRFHeader must be present on every state-changing request authenticated by the cookie.
	CSRFHeader = "X-Dojo-Csrf"

	sessionTTL = 30 * 24 * time.Hour
	// touchEvery bounds the sliding-expiry write: at most one UPDATE per session per interval.
	touchEvery = time.Minute
	// tokenLen is the length of 32 random bytes in unpadded base64url.
	tokenLen = 43
)

// SessionStore is the data the session middleware and /v1/me need.
type SessionStore interface {
	GetActiveSession(ctx context.Context, tokenHash []byte) (repo.GetActiveSessionRow, error)
	TouchSession(ctx context.Context, id pgtype.UUID) error
	GetLearnerByID(ctx context.Context, id pgtype.UUID) (repo.Learner, error)
}

// Auth authenticates requests by session cookie.
type Auth struct {
	Store  SessionStore
	Secure bool // cookie Secure flag, from PUBLIC_ORIGIN
}

// ParseOrigin validates PUBLIC_ORIGIN for the environment (local, pre or pro) and reports
// whether session cookies must be Secure. pre and pro refuse a non-https origin (design 3.1).
func ParseOrigin(env, origin string) (secure bool, err error) {
	switch env {
	case "local", "pre", "pro":
	default:
		return false, fmt.Errorf("DOJO_ENV must be local, pre or pro, got %q", env)
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return false, fmt.Errorf("PUBLIC_ORIGIN must be an http or https origin, got %q", origin)
	}
	if u.Scheme != "https" && env != "local" {
		return false, fmt.Errorf("PUBLIC_ORIGIN must be https in %s, got %q", env, origin)
	}
	return u.Scheme == "https", nil
}

type principal struct {
	learner   pgtype.UUID
	session   pgtype.UUID
	suspended bool
}

type ctxKey struct{}

func principalOf(ctx context.Context) (principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(principal)
	return p, ok
}

// HashToken is how a session token is stored: sha256 of the cookie value as sent.
func HashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// Session resolves the cookie into a principal on the request context. It never rejects:
// routes that need a learner use requireSession. A request with an Authorization header
// ignores the cookie (one credential only).
func (a Auth) Session(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			if p, ok := a.lookup(r); ok {
				r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, p))
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (a Auth) lookup(r *http.Request) (principal, bool) {
	c, err := r.Cookie(SessionCookie)
	if err != nil || len(c.Value) != tokenLen {
		return principal{}, false
	}
	row, err := a.Store.GetActiveSession(r.Context(), HashToken(c.Value))
	if err != nil {
		return principal{}, false
	}
	if time.Since(row.LastUsedAt.Time) > touchEvery {
		if err := a.Store.TouchSession(context.WithoutCancel(r.Context()), row.ID); err != nil {
			slog.Warn("touch session failed", "error", err)
		}
	}
	return principal{learner: row.LearnerID, session: row.ID, suspended: row.SuspendedAt.Valid}, true
}

// CSRF requires a JSON content type and the custom header on state-changing requests
// authenticated by the cookie. Requests without a cookie principal (bearer, anonymous) pass.
func CSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// start is a browser form POST that acts on no session; a signed-in browser must still reach it.
		if _, ok := principalOf(r.Context()); ok && !safeMethod(r.Method) && r.URL.Path != startPath {
			mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || mt != "application/json" || r.Header.Get(CSRFHeader) == "" {
				writeError(w, http.StatusForbidden, "csrf_rejected")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// requireSession passes only an active, unsuspended learner.
func requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := principalOf(r.Context())
		switch {
		case !ok:
			writeError(w, http.StatusUnauthorized, "unauthenticated")
		case p.suspended:
			writeError(w, http.StatusForbidden, "account_suspended")
		default:
			next(w, r)
		}
	}
}

func writeError(w http.ResponseWriter, code int, name string) {
	writeJSON(w, code, map[string]string{"error": name})
}

type meResponse struct {
	ID               string  `json:"id"`
	Username         string  `json:"username"`
	GithubLogin      string  `json:"github_login"`
	Email            *string `json:"email"`
	Language         string  `json:"language"`
	UsageStats       bool    `json:"usage_stats"`
	AnalyticsConsent *bool   `json:"analytics_consent"`
}

func (a Auth) me(w http.ResponseWriter, r *http.Request) {
	p, _ := principalOf(r.Context())
	l, err := a.Store.GetLearnerByID(r.Context(), p.learner)
	if errors.Is(err, context.Canceled) {
		return
	}
	if err != nil {
		// A learner deleted since the session lookup is the only expected miss.
		slog.Warn("load learner failed", "error", err)
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	out := meResponse{
		ID: l.ID.String(), Username: l.Username, GithubLogin: l.GithubLogin,
		Language: l.Language, UsageStats: l.UsageStats,
	}
	if l.Email.Valid {
		out.Email = &l.Email.String
	}
	if l.AnalyticsConsent.Valid {
		out.AnalyticsConsent = &l.AnalyticsConsent.Bool
	}
	writeJSON(w, http.StatusOK, out)
}

// SetCookie returns the session cookie for a freshly generated token.
func (a Auth) SetCookie(token string) *http.Cookie {
	return &http.Cookie{ //nolint:gosec // Secure follows PUBLIC_ORIGIN by design (3.1)
		Name: SessionCookie, Value: token, Path: "/", MaxAge: int(sessionTTL.Seconds()),
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: a.Secure,
	}
}

// ClearCookie returns the cookie that removes the session cookie.
func (a Auth) ClearCookie() *http.Cookie {
	c := a.SetCookie("") //nolint:gosec // see SetCookie
	c.MaxAge = -1
	return c
}
