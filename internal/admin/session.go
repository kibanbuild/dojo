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
// Every request re-reads the session and its operator (ActiveSession joins operators.revoked_at), so
// a revoked operator or session is out at the next request. Every non-GET request must then carry
// the session's CSRF token (csrfProtect). Handlers read the token with csrfFrom.
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
		csrf := csrfTokenFor(c.Value)
		ctx := context.WithValue(context.WithValue(r.Context(), operatorKey{}, op), csrfKey{}, csrf)
		csrfProtect(csrf, next)(w, r.WithContext(ctx))
	}
}

var landingPage = template.Must(template.New("landing").Parse(layoutTemplates + `{{template "top" .}}<header class="bar"><div class="bar-in"><div class="bar-l">{{template "brand"}}
<nav class="nav" aria-label="Operator tools"><a href="/learners">Learners</a></nav></div>
<form class="signout" method="post" action="/signout"><input type="hidden" name="csrf_token" value="{{.CSRF}}"><button type="submit">Sign out</button></form></div>{{template "note"}}</header>
<main><h1>Dojo operator</h1>
<p class="lede">Signed in as {{.Login}}</p>
</main></body></html>
`))

func (s *signIn) landing(w http.ResponseWriter, r *http.Request) {
	render(w, http.StatusOK, landingPage, map[string]string{"Title": "Home", "Login": operatorFrom(r.Context()).Login, "CSRF": csrfFrom(r.Context())})
}

// signout revokes the session that made the request. It is behind authenticated, so it needs the
// CSRF token: a page on another 127.0.0.1 port (same site, Lax does not stop it) cannot force it.
// Without a live session the request is redirected to the sign-in page before it gets here.
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
