// Package fakegithub is a small stand-in for GitHub's OAuth web flow and the user endpoints.
// It lives in its own binary (cmd/fake-github) and is never linked into dojo-api.
package fakegithub

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxBody = 64 << 10
	codeTTL = 10 * time.Minute // GitHub's authorization codes live ten minutes
	docsURL = "https://docs.github.com/rest"
)

// Client is a registered OAuth App.
type Client struct {
	ID, Secret   string
	RedirectURIs []string
	DeviceFlow   bool // GitHub requires device flow to be enabled per OAuth App
}

// Email is one entry of GET /user/emails.
type Email struct {
	Email    string `json:"email"`
	Primary  bool   `json:"primary"`
	Verified bool   `json:"verified"`
}

// User is a seeded account.
type User struct {
	ID     int64
	Login  string
	Name   string
	Email  string // the public profile email; empty means null
	TwoFA  bool
	Emails []Email
}

// Config seeds the fake. Now is injectable for expiry tests.
type Config struct {
	Clients []Client
	Users   []User
	Now     func() time.Time

	// PublicURL is the address browsers use to reach the fake; it fills verification_uri.
	// Empty means derive it from the request's Host header.
	PublicURL string
	// DeviceInterval is the minimum polling interval in the device flow (GitHub: 5s). Zero means 5s.
	DeviceInterval time.Duration
	// DeviceExpiry is how long a device code lives (GitHub: 900s). Zero means 15 minutes.
	DeviceExpiry time.Duration
	// TokenTTL makes issued access tokens expire. Zero means they never do (OAuth App tokens do not expire).
	TokenTTL time.Duration
}

// DefaultUsers covers the sign-in cases: 2FA on, 2FA off, and no public or verified email.
func DefaultUsers() []User {
	return []User{
		{ID: 1001, Login: "ada", Name: "Ada Lovelace", Email: "ada@example.test", TwoFA: true,
			Emails: []Email{{"ada@example.test", true, true}}},
		{ID: 1002, Login: "bob", Name: "Bob Builder", Email: "bob@example.test", TwoFA: false,
			Emails: []Email{{"bob@example.test", true, true}}},
		{ID: 1003, Login: "cy", Name: "Cy Noemail", TwoFA: true,
			Emails: []Email{{"cy@example.test", true, false}}},
		// Logins the username CHECK refuses as they are: 1 and 39 characters (design 3.1).
		{ID: 1004, Login: "z", Name: "Zed Short", Email: "z@example.test", TwoFA: true,
			Emails: []Email{{"z@example.test", true, true}}},
		{ID: 1005, Login: "a-very-long-github-login-of-39-chars-xx", Name: "Long Login", Email: "long@example.test", TwoFA: true,
			Emails: []Email{{"long@example.test", true, true}}},
	}
}

type grant struct {
	clientID, redirectURI, challenge, login string
	scopes                                  []string
	expires                                 time.Time
}

type token struct {
	clientID, login string
	scopes          []string
	created         time.Time
	expires         time.Time // zero = never
}

type server struct {
	cfg    Config
	users  map[string]User
	mu     sync.Mutex
	codes  map[string]grant
	tokens map[string]token
	device map[string]*deviceGrant // by device_code
}

// New returns the fake GitHub handler.
func New(cfg Config) http.Handler {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	s := &server{cfg: cfg, users: map[string]User{}, codes: map[string]grant{}, tokens: map[string]token{}, device: map[string]*deviceGrant{}}
	for _, u := range cfg.Users {
		s.users[u.Login] = u
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login/oauth/authorize", s.authorize)
	mux.HandleFunc("POST /login/oauth/access_token", s.accessToken)
	mux.HandleFunc("GET /user", s.user)
	mux.HandleFunc("GET /user/emails", s.emails)
	mux.HandleFunc("GET /users/{login}", s.publicUser)
	mux.HandleFunc("POST /login/device/code", s.deviceCode)
	mux.HandleFunc("GET /login/device", s.devicePage)
	mux.HandleFunc("POST /login/device", s.deviceDecide)
	mux.HandleFunc("POST /applications/{client_id}/token", s.checkToken)
	mux.HandleFunc("DELETE /applications/{client_id}/token", s.revoke)
	return mux
}

func (s *server) client(id string) (Client, bool) {
	for _, c := range s.cfg.Clients {
		if c.ID == id {
			return c, true
		}
	}
	return Client{}, false
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func splitScopes(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == ',' })
}

// has reports whether the scopes include want or its parent "user" scope.
func has(scopes []string, want string) bool {
	return slices.Contains(scopes, want) || slices.Contains(scopes, "user")
}

var chooser = template.Must(template.New("c").Parse(`<!doctype html><title>Fake GitHub</title>
<h1>Sign in as</h1>{{range $u := .Users}}
<form method="get" action="/login/oauth/authorize">{{range $k, $v := $.Query}}<input type="hidden" name="{{$k}}" value="{{index $v 0}}">{{end}}
<button name="login" value="{{$u.Login}}">{{$u.Login}} ({{$u.Name}})</button></form>{{end}}`))

func (s *server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	c, ok := s.client(q.Get("client_id"))
	i := -1
	if ok {
		i = slices.Index(c.RedirectURIs, q.Get("redirect_uri"))
	}
	if i < 0 {
		http.Error(w, "404 unknown client or redirect_uri", http.StatusNotFound)
		return
	}
	// Redirect to the registered copy, never the request's value.
	redirect, err := url.Parse(c.RedirectURIs[i])
	if err != nil {
		http.Error(w, "bad redirect_uri", http.StatusBadRequest)
		return
	}
	back := func(vals url.Values) {
		if st := q.Get("state"); st != "" {
			vals.Set("state", st)
		}
		redirect.RawQuery = vals.Encode()
		http.Redirect(w, r, redirect.String(), http.StatusFound)
	}
	challenge := q.Get("code_challenge")
	if challenge != "" && q.Get("code_challenge_method") != "S256" {
		back(url.Values{"error": {"invalid_request"}, "error_description": {"code_challenge_method must be S256"}})
		return
	}
	login := q.Get("login")
	if login == "" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = chooser.Execute(w, map[string]any{"Users": s.cfg.Users, "Query": q})
		return
	}
	if _, ok := s.users[login]; !ok {
		http.Error(w, "unknown login", http.StatusBadRequest)
		return
	}
	scopes := splitScopes(q.Get("scope"))
	if q.Has("grant") { // the user grants fewer scopes than requested
		allowed := splitScopes(q.Get("grant"))
		scopes = slices.DeleteFunc(scopes, func(sc string) bool { return !slices.Contains(allowed, sc) })
	}
	code := randHex(10)
	s.mu.Lock()
	s.codes[code] = grant{c.ID, c.RedirectURIs[i], challenge, login, scopes, s.cfg.Now().Add(codeTTL)}
	s.mu.Unlock()
	back(url.Values{"code": {code}})
}

func oauthError(w http.ResponseWriter, r *http.Request, code, desc string) {
	writeOAuth(w, r, oauthErrorBody(code, desc))
}

func oauthErrorBody(code, desc string) map[string]any {
	return map[string]any{"error": code, "error_description": desc,
		"error_uri": "https://docs.github.com/apps/oauth-apps/maintaining-oauth-apps/troubleshooting-oauth-app-access-token-request-errors/#" + strings.ReplaceAll(code, "_", "-")}
}

// writeOAuth answers JSON when asked for it and form-encoded otherwise, always with status 200.
func writeOAuth(w http.ResponseWriter, r *http.Request, body map[string]any) {
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(body)
		return
	}
	v := url.Values{}
	for k, val := range body {
		v.Set(k, fmt.Sprint(val))
	}
	w.Header().Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	_, _ = w.Write([]byte(v.Encode()))
}

func (s *server) accessToken(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	var p map[string]string
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			oauthError(w, r, "invalid_request", "The request body is not valid JSON.")
			return
		}
	} else {
		if err := r.ParseForm(); err != nil {
			oauthError(w, r, "invalid_request", "The request body is not valid.")
			return
		}
		p = map[string]string{}
		for k := range r.PostForm {
			p[k] = r.PostForm.Get(k)
		}
	}
	switch p["grant_type"] {
	case "", "authorization_code": // real GitHub and x/oauth2 send grant_type=authorization_code on the web exchange
	case deviceGrantType:
		s.devicePoll(w, r, p)
		return
	default:
		oauthError(w, r, "unsupported_grant_type", "The grant type is not supported.")
		return
	}
	c, ok := s.client(p["client_id"])
	if !ok || subtle.ConstantTimeCompare([]byte(c.Secret), []byte(p["client_secret"])) != 1 {
		oauthError(w, r, "incorrect_client_credentials", "The client_id and/or client_secret passed are incorrect.")
		return
	}
	s.mu.Lock()
	g, found := s.codes[p["code"]]
	delete(s.codes, p["code"]) // single use, even when the exchange then fails
	s.mu.Unlock()
	if !found || g.clientID != c.ID || s.cfg.Now().After(g.expires) {
		oauthError(w, r, "bad_verification_code", "The code passed is incorrect or expired.")
		return
	}
	if ru := p["redirect_uri"]; ru != "" && ru != g.redirectURI {
		oauthError(w, r, "redirect_uri_mismatch", "The redirect_uri MUST match the registered callback URL for this application.")
		return
	}
	if g.challenge != "" {
		sum := sha256.Sum256([]byte(p["code_verifier"]))
		if subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(g.challenge)) != 1 {
			oauthError(w, r, "bad_verification_code", "The code_verifier does not match the code_challenge.")
			return
		}
	}
	writeOAuth(w, r, s.issue(c.ID, g.login, g.scopes))
}

// issue mints an access token and returns the access_token response body.
func (s *server) issue(clientID, login string, scopes []string) map[string]any {
	now := s.cfg.Now()
	t := token{clientID: clientID, login: login, scopes: scopes, created: now}
	if s.cfg.TokenTTL > 0 {
		t.expires = now.Add(s.cfg.TokenTTL)
	}
	tok := "gho_" + randHex(18)
	s.mu.Lock()
	s.tokens[tok] = t
	s.mu.Unlock()
	return map[string]any{"access_token": tok, "scope": strings.Join(scopes, ","), "token_type": "bearer"}
}

// live looks a token up, treating an expired one as gone. The caller holds s.mu.
func (s *server) live(tok string) (token, bool) {
	t, ok := s.tokens[tok]
	if ok && !t.expires.IsZero() && !s.cfg.Now().Before(t.expires) {
		return token{}, false
	}
	return t, ok
}

func apiError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"message": msg, "documentation_url": docsURL, "status": strconv.Itoa(status)})
}

// authed resolves the bearer token and sets X-OAuth-Scopes; it answers 401 itself on failure.
func (s *server) authed(w http.ResponseWriter, r *http.Request) (token, bool) {
	scheme, val, _ := strings.Cut(r.Header.Get("Authorization"), " ")
	s.mu.Lock()
	t, ok := s.live(strings.TrimSpace(val))
	s.mu.Unlock()
	if !ok || (!strings.EqualFold(scheme, "bearer") && !strings.EqualFold(scheme, "token")) {
		apiError(w, http.StatusUnauthorized, "Bad credentials")
		return token{}, false
	}
	w.Header().Set("X-OAuth-Scopes", strings.Join(t.scopes, ", "))
	return t, true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *server) user(w http.ResponseWriter, r *http.Request) {
	t, ok := s.authed(w, r)
	if !ok {
		return
	}
	u := s.users[t.login]
	out := map[string]any{"id": u.ID, "login": u.Login, "name": u.Name, "email": nil}
	if u.Email != "" {
		out["email"] = u.Email
	}
	if has(t.scopes, "read:user") {
		out["two_factor_authentication"] = u.TwoFA
	}
	writeJSON(w, out)
}

// publicUser is GET /users/{login}: no token, case-insensitive, the public fields only.
func (s *server) publicUser(w http.ResponseWriter, r *http.Request) {
	want := r.PathValue("login")
	for _, u := range s.users {
		if strings.EqualFold(u.Login, want) {
			writeJSON(w, map[string]any{"id": u.ID, "login": u.Login, "type": "User"})
			return
		}
	}
	apiError(w, http.StatusNotFound, "Not Found")
}

func (s *server) emails(w http.ResponseWriter, r *http.Request) {
	t, ok := s.authed(w, r)
	if !ok {
		return
	}
	if !has(t.scopes, "user:email") {
		apiError(w, http.StatusNotFound, "Not Found")
		return
	}
	writeJSON(w, s.users[t.login].Emails)
}

func (s *server) revoke(w http.ResponseWriter, r *http.Request) {
	id, secret, ok := r.BasicAuth()
	c, found := s.client(id)
	if !ok || !found || subtle.ConstantTimeCompare([]byte(c.Secret), []byte(secret)) != 1 {
		apiError(w, http.StatusUnauthorized, "Bad credentials")
		return
	}
	if r.PathValue("client_id") != id {
		apiError(w, http.StatusNotFound, "Not Found")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.AccessToken == "" {
		apiError(w, http.StatusUnprocessableEntity, "Validation Failed")
		return
	}
	s.mu.Lock()
	t, exists := s.live(body.AccessToken)
	if exists && t.clientID == id {
		delete(s.tokens, body.AccessToken)
	} else {
		exists = false
	}
	s.mu.Unlock()
	if !exists {
		apiError(w, http.StatusNotFound, "Not Found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
