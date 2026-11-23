package admin

import (
	"context"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"time"
)

const (
	// sessionCookie differs from the public session cookie's name: cookies ignore ports.
	sessionCookie = "dojo_admin_session"
	// sessionTTL matches the one hour the admin_sessions CHECK fixes; there is no sliding.
	sessionTTL = time.Hour
)

// sessionCookieFor is HttpOnly, host-only (no Domain) and Lax as design 3.7 says: Strict would drop
// the cookie on the redirect that follows GitHub's cross-site callback. Secure follows
// Deps.SecureCookies: off today, the service is plain HTTP on loopback inside the tunnel.
func (s *signIn) sessionCookieFor(v string, maxAge int) *http.Cookie {
	return &http.Cookie{ //nolint:gosec // Secure follows Deps.SecureCookies by design (3.7)
		Name: sessionCookie, Value: v, Path: "/", MaxAge: maxAge, HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: s.SecureCookies}
}

type operatorKey struct{}

// operatorFrom returns the operator an authenticated handler runs for.
func operatorFrom(ctx context.Context) Operator {
	op, _ := ctx.Value(operatorKey{}).(Operator)
	return op
}

// authenticated runs next only for a live session: not revoked, not expired, operator not revoked.
// Anything else clears the cookie and goes to the sign-in page.
//
// 12d seam: the per-request operator check, the per-session CSRF token on state-changing forms and
// the audit helper hang off this wrapper.
func (s *signIn) authenticated(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil {
			toSignin(w, r, "")
			return
		}
		op, err := s.Store.ActiveSession(r.Context(), hashToken(c.Value))
		if err != nil {
			if !errors.Is(err, ErrNotFound) {
				slog.Error("operator session lookup failed", "error", err)
			}
			http.SetCookie(w, s.sessionCookieFor("", -1))
			toSignin(w, r, "")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), operatorKey{}, op)))
	}
}

var landingPage = template.Must(template.New("landing").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Dojo operator</title>
<meta name="viewport" content="width=device-width, initial-scale=1"></head>
<body><main><h1>Dojo operator</h1>
<p>Signed in as {{.Login}}</p>
<form method="post" action="/signout"><button type="submit">Sign out</button></form>
</main></body></html>
`))

func (s *signIn) landing(w http.ResponseWriter, r *http.Request) {
	render(w, http.StatusOK, landingPage, map[string]string{"Login": operatorFrom(r.Context()).Login})
}

// signout revokes the session that made the request. Lax stops cross-site POSTs but not a page on
// another 127.0.0.1 port (same site), so today a forced sign-out is possible; the CSRF token of 12d
// closes it and must land before any other state-changing form.
func (s *signIn) signout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if err := s.Store.RevokeSession(r.Context(), hashToken(c.Value)); err != nil {
			slog.Error("operator sign-out: revoke failed", "error", err)
			http.Error(w, "sign-out failed", http.StatusInternalServerError)
			return
		}
	}
	http.SetCookie(w, s.sessionCookieFor("", -1))
	toSignin(w, r, "")
}

func render(w http.ResponseWriter, status int, t *template.Template, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := t.Execute(w, data); err != nil {
		slog.Error("render failed", "error", err)
	}
}
