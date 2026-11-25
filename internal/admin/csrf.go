package admin

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"log/slog"
	"net/http"
)

const (
	// csrfField is the hidden form field every state-changing form carries.
	csrfField = "csrf_token"
	// csrfHeader is accepted as an alternative to the field, for non-form callers.
	csrfHeader = "X-CSRF-Token"
	// maxFormBytes bounds a form body read by the CSRF check.
	maxFormBytes = 64 << 10
)

type csrfKey struct{}

// csrfFrom returns the CSRF token of the session an authenticated handler runs for; templates put
// it in a hidden input named csrf_token.
func csrfFrom(ctx context.Context) string {
	t, _ := ctx.Value(csrfKey{}).(string)
	return t
}

// csrfTokenFor derives the per-session token: HMAC-SHA256 keyed by the raw session token over a
// fixed label. The raw token lives only in the HttpOnly cookie (the database holds its hash), so the
// token needs no storage and no server key, dies with the session, and a page cannot compute it
// without already holding the cookie. It is not the cookie value, so rendering it leaks nothing.
func csrfTokenFor(sessionToken string) string {
	m := hmac.New(sha256.New, []byte(sessionToken))
	m.Write([]byte("dojo-admin-csrf-v1"))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// csrfProtect refuses a state-changing request that does not carry the session's token. It is a
// second layer under SameSite=Lax, which does not stop a page on another 127.0.0.1 port (same site).
// Order of checks: browser-reported provenance first (Sec-Fetch-Site, Origin), then the token, which
// is the one that cannot be absent. Answers 403 with an empty body.
func csrfProtect(want string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next(w, r)
			return
		}
		if !sameOrigin(r) {
			slog.Warn("operator request refused: cross-origin", "route", r.Pattern)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
		got := r.Header.Get(csrfHeader)
		if got == "" {
			if err := r.ParseForm(); err == nil {
				got = r.PostForm.Get(csrfField)
			}
		}
		if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			slog.Warn("operator request refused: bad csrf token", "route", r.Pattern)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// sameOrigin is the cheap provenance layer. A header the browser did not send passes (the token
// still decides); one it sent must say same origin. Under Referrer-Policy: no-referrer, Chrome sends
// Origin: null on a same-origin form post, so null passes only when Sec-Fetch-Site vouches for it.
func sameOrigin(r *http.Request) bool {
	site := r.Header.Get("Sec-Fetch-Site")
	switch site {
	case "", "same-origin", "none":
	default:
		return false
	}
	switch o := r.Header.Get("Origin"); o {
	case "", "http://" + Host, "https://" + Host:
		return true
	case "null":
		return site == "same-origin"
	default:
		return false
	}
}
