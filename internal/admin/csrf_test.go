package admin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func (e *env) post(path string, form url.Values, hdr map[string]string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Host = Host
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

var csrfInput = regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)

func pageToken(t *testing.T, e *env, s *http.Cookie) string {
	t.Helper()
	m := csrfInput.FindStringSubmatch(e.do(http.MethodGet, "/", s).Body.String())
	if m == nil {
		t.Fatal("landing page has no csrf_token input")
	}
	return m[1]
}

func sessionAlive(e *env, s *http.Cookie) bool {
	return e.do(http.MethodGet, "/", s).Code == http.StatusOK
}

func TestSignoutNeedsCSRFToken(t *testing.T) {
	e := newEnv(t)
	s := signedIn(t, e)
	tok := pageToken(t, e, s)

	refused := []struct {
		name string
		form url.Values
		hdr  map[string]string
	}{
		{"no token", nil, nil},
		{"empty token", url.Values{csrfField: {""}}, nil},
		{"wrong token", url.Values{csrfField: {"nope"}}, nil},
		{"token of the cookie value", url.Values{csrfField: {s.Value}}, nil},
		{"cross-site fetch metadata", url.Values{csrfField: {tok}}, map[string]string{"Sec-Fetch-Site": "cross-site"}},
		{"same-site fetch metadata", url.Values{csrfField: {tok}}, map[string]string{"Sec-Fetch-Site": "same-site"}},
		{"other-port origin", url.Values{csrfField: {tok}}, map[string]string{"Origin": "http://127.0.0.1:9999"}},
		{"null origin", url.Values{csrfField: {tok}}, map[string]string{"Origin": "null"}},
		{"null origin, cross-site", url.Values{csrfField: {tok}}, map[string]string{"Origin": "null", "Sec-Fetch-Site": "cross-site"}},
		{"null origin, same-site", url.Values{csrfField: {tok}}, map[string]string{"Origin": "null", "Sec-Fetch-Site": "same-site"}},
		{"null origin, same-origin, wrong token", url.Values{csrfField: {"nope"}}, map[string]string{"Origin": "null", "Sec-Fetch-Site": "same-origin"}},
		{"wrong token in header", nil, map[string]string{csrfHeader: "nope"}},
	}
	for _, tt := range refused {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.post("/signout", tt.form, tt.hdr, s)
			if rec.Code != http.StatusForbidden || rec.Body.Len() != 0 {
				t.Fatalf("got %d %q", rec.Code, rec.Body.String())
			}
			if !sessionAlive(e, s) {
				t.Fatal("a refused request ended the session")
			}
		})
	}

	t.Run("token in the form", func(t *testing.T) {
		e := newEnv(t)
		s := signedIn(t, e)
		rec := e.post("/signout", url.Values{csrfField: {pageToken(t, e, s)}},
			map[string]string{"Origin": "http://" + Host, "Sec-Fetch-Site": "same-origin"}, s)
		wantRedirect(t, rec, "/signin")
		if sessionAlive(e, s) {
			t.Fatal("session survived sign-out")
		}
	})
	t.Run("chrome form post under no-referrer", func(t *testing.T) {
		e := newEnv(t)
		s := signedIn(t, e)
		rec := e.post("/signout", url.Values{csrfField: {pageToken(t, e, s)}},
			map[string]string{"Origin": "null", "Sec-Fetch-Site": "same-origin"}, s)
		wantRedirect(t, rec, "/signin")
	})
	t.Run("token in the header", func(t *testing.T) {
		e := newEnv(t)
		s := signedIn(t, e)
		wantRedirect(t, e.post("/signout", nil, map[string]string{csrfHeader: pageToken(t, e, s)}, s), "/signin")
	})
}

func TestCSRFTokenIsPerSession(t *testing.T) {
	e := newEnv(t)
	a, b := signedIn(t, e), signedIn(t, e)
	ta, tb := pageToken(t, e, a), pageToken(t, e, b)
	if ta == tb || ta == a.Value {
		t.Fatalf("tokens not distinct: %q %q", ta, tb)
	}
	if ta != pageToken(t, e, a) {
		t.Fatal("token changes between pages of one session")
	}
	// Session A's token does not unlock session B.
	if rec := e.post("/signout", url.Values{csrfField: {ta}}, nil, b); rec.Code != http.StatusForbidden {
		t.Fatalf("cross-session token: %d", rec.Code)
	}
}

// The session check runs before the CSRF check, so a request with no session still goes to sign-in.
func TestPostWithoutSessionGoesToSignin(t *testing.T) {
	e := newEnv(t)
	wantRedirect(t, e.post("/signout", nil, nil), "/signin")
}

// Every request re-reads the session and operator: the next one after a revoke is locked out, GET or POST.
func TestRevocationLocksOutTheNextRequest(t *testing.T) {
	t.Run("operator revoked", func(t *testing.T) {
		e := newEnv(t)
		s := signedIn(t, e)
		tok := pageToken(t, e, s)
		e.store.operators[1001].revoked = true
		wantRedirect(t, e.do(http.MethodGet, "/", s), "/signin")
		// Even a request with a valid token is redirected, not served.
		wantRedirect(t, e.post("/signout", url.Values{csrfField: {tok}}, nil, s), "/signin")
	})
	t.Run("session revoked", func(t *testing.T) {
		e := newEnv(t)
		s := signedIn(t, e)
		if !sessionAlive(e, s) {
			t.Fatal("not alive")
		}
		for _, ss := range e.store.sessions {
			ss.revoked = true
		}
		wantRedirect(t, e.do(http.MethodGet, "/", s), "/signin")
	})
}
