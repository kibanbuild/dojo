package fakegithub

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"html/template"
	"net/http"
	"slices"
	"strings"
	"time"
)

// Everything in this file mirrors GitHub's documented device flow
// (docs.github.com/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps#device-flow)
// and the check-token endpoint. Where the docs are silent the choice is marked GUESS.

const (
	deviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"
	userCodeAlpha   = "BCDFGHJKLMNPQRSTVWXZ" // GUESS: GitHub's user codes are 8 letters as XXXX-XXXX; the alphabet is not documented
)

type deviceGrant struct {
	clientID  string
	userCode  string
	scopes    []string
	expires   time.Time
	interval  time.Duration
	lastPoll  time.Time
	state     string // "", "approved", "denied"
	login     string
	approvedS []string
}

func (s *server) deviceInterval() time.Duration {
	if s.cfg.DeviceInterval > 0 {
		return s.cfg.DeviceInterval
	}
	return 5 * time.Second
}

func (s *server) deviceExpiry() time.Duration {
	if s.cfg.DeviceExpiry > 0 {
		return s.cfg.DeviceExpiry
	}
	return 15 * time.Minute
}

func newUserCode() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	out := make([]byte, 0, 9)
	for i, v := range b {
		if i == 4 {
			out = append(out, '-')
		}
		out = append(out, userCodeAlpha[int(v)%len(userCodeAlpha)])
	}
	return string(out)
}

// formParams reads the request parameters from a JSON or form body, like the token endpoint.
func formParams(w http.ResponseWriter, r *http.Request) (map[string]string, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	p := map[string]string{}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			oauthError(w, r, "invalid_request", "The request body is not valid JSON.")
			return nil, false
		}
		return p, true
	}
	if err := r.ParseForm(); err != nil {
		oauthError(w, r, "invalid_request", "The request body is not valid.")
		return nil, false
	}
	for k := range r.PostForm {
		p[k] = r.PostForm.Get(k)
	}
	return p, true
}

func (s *server) publicURL(r *http.Request) string {
	if s.cfg.PublicURL != "" {
		return strings.TrimRight(s.cfg.PublicURL, "/")
	}
	return "http://" + r.Host
}

// deviceCode is POST /login/device/code.
func (s *server) deviceCode(w http.ResponseWriter, r *http.Request) {
	p, ok := formParams(w, r)
	if !ok {
		return
	}
	c, found := s.client(p["client_id"])
	if !found {
		// GUESS: GitHub answers an unknown client_id on this endpoint with incorrect_client_credentials.
		oauthError(w, r, "incorrect_client_credentials", "The client_id passed is incorrect.")
		return
	}
	if !c.DeviceFlow {
		oauthError(w, r, "device_flow_disabled", "Device flow must be explicitly enabled for this App.")
		return
	}
	now := s.cfg.Now()
	g := &deviceGrant{clientID: c.ID, userCode: newUserCode(), scopes: splitScopes(p["scope"]),
		expires: now.Add(s.deviceExpiry()), interval: s.deviceInterval()}
	dc := randHex(20) // GitHub's device codes are 40 hex characters
	s.mu.Lock()
	for k, old := range s.device { // sweep grants dead for over an hour; recent ones still answer expired_token
		if now.Sub(old.expires) > time.Hour {
			delete(s.device, k)
		}
	}
	s.device[dc] = g
	s.mu.Unlock()
	writeOAuth(w, r, map[string]any{
		"device_code": dc, "user_code": g.userCode,
		"verification_uri": s.publicURL(r) + "/login/device",
		"expires_in":       int(s.deviceExpiry().Seconds()), "interval": int(g.interval.Seconds()),
	})
}

// devicePoll answers the device_code grant on POST /login/oauth/access_token.
func (s *server) devicePoll(w http.ResponseWriter, r *http.Request, p map[string]string) {
	c, ok := s.client(p["client_id"])
	if !ok {
		oauthError(w, r, "incorrect_client_credentials", "The client_id passed is incorrect.")
		return
	}
	now := s.cfg.Now()
	s.mu.Lock()
	g, found := s.device[p["device_code"]]
	if !found || g.clientID != c.ID {
		s.mu.Unlock()
		oauthError(w, r, "incorrect_device_code", "The device_code provided is not valid.")
		return
	}
	if now.After(g.expires) {
		s.mu.Unlock()
		oauthError(w, r, "expired_token", "The device_code has expired.")
		return
	}
	// Polling faster than the interval earns slow_down and a longer interval; the early poll still
	// counts as a poll, so a client that keeps hammering never recovers.
	early := !g.lastPoll.IsZero() && now.Sub(g.lastPoll) < g.interval // the first poll is never early
	g.lastPoll = now
	if early {
		g.interval += 5 * time.Second // documented: slow_down adds 5 seconds
		iv := int(g.interval.Seconds())
		s.mu.Unlock()
		body := oauthErrorBody("slow_down", "Too many requests have been made in the same timeframe.")
		body["interval"] = iv
		writeOAuth(w, r, body)
		return
	}
	switch g.state {
	case "denied":
		delete(s.device, p["device_code"])
		s.mu.Unlock()
		oauthError(w, r, "access_denied", "The user has denied the request.")
	case "approved":
		delete(s.device, p["device_code"]) // GUESS: a device code is single use; a second poll is incorrect_device_code
		login, scopes := g.login, g.approvedS
		s.mu.Unlock()
		writeOAuth(w, r, s.issue(c.ID, login, scopes))
	default:
		s.mu.Unlock()
		oauthError(w, r, "authorization_pending", "The authorization request is still pending.")
	}
}

var devicePage = template.Must(template.New("d").Parse(`<!doctype html><title>Fake GitHub device login</title>
<h1>Device activation</h1>{{if .Msg}}<p>{{.Msg}}</p>{{end}}
<form method="post" action="/login/device"><label>Code <input name="user_code" value="{{.Code}}"></label>
<label>Sign in as <select name="login">{{range .Users}}<option value="{{.Login}}">{{.Login}} ({{.Name}})</option>{{end}}</select></label>
<button name="decision" value="approve">Authorize</button><button name="decision" value="deny">Cancel</button></form>`))

func (s *server) devicePage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = devicePage.Execute(w, map[string]any{"Users": s.cfg.Users, "Code": r.URL.Query().Get("user_code"), "Msg": ""})
}

// deviceDecide is the page's form target and also the test hook: POST /login/device with form fields
// user_code, login, decision (approve|deny) and an optional grant (space or comma separated scopes the
// user keeps, like the web flow's grant parameter). It needs no HTML scraping. The hook is the fake's
// own route, so it is reachable only where the fake itself is.
func (s *server) deviceDecide(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	code := strings.ToUpper(strings.TrimSpace(r.PostForm.Get("user_code")))
	if len(code) == 8 {
		code = code[:4] + "-" + code[4:]
	}
	login := r.PostForm.Get("login")
	decision := r.PostForm.Get("decision")
	if _, ok := s.users[login]; !ok {
		http.Error(w, "unknown login", http.StatusBadRequest)
		return
	}
	if decision != "approve" && decision != "deny" {
		http.Error(w, "decision must be approve or deny", http.StatusBadRequest)
		return
	}
	now := s.cfg.Now()
	s.mu.Lock()
	var g *deviceGrant
	for _, d := range s.device {
		if subtle.ConstantTimeCompare([]byte(d.userCode), []byte(code)) == 1 {
			g = d
		}
	}
	if g == nil || now.After(g.expires) || g.state != "" {
		s.mu.Unlock()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_ = devicePage.Execute(w, map[string]any{"Users": s.cfg.Users, "Code": code, "Msg": "That code is not valid or has expired."})
		return
	}
	if decision == "deny" {
		g.state = "denied"
	} else {
		g.state, g.login = "approved", login
		g.approvedS = g.scopes
		if r.PostForm.Has("grant") {
			allowed := splitScopes(r.PostForm.Get("grant"))
			g.approvedS = slices.DeleteFunc(slices.Clone(g.scopes), func(sc string) bool { return !slices.Contains(allowed, sc) })
		}
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte("<!doctype html><title>Fake GitHub</title><p>You can return to your device.</p>"))
}

// checkToken is POST /applications/{client_id}/token.
func (s *server) checkToken(w http.ResponseWriter, r *http.Request) {
	id, secret, ok := r.BasicAuth()
	c, found := s.client(id)
	if !ok || !found || subtle.ConstantTimeCompare([]byte(c.Secret), []byte(secret)) != 1 {
		apiError(w, http.StatusUnauthorized, "Bad credentials") // GUESS: same answer as the revoke endpoint
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
	s.mu.Unlock()
	if !exists || t.clientID != id { // another app's token looks unknown, like a revoked one
		apiError(w, http.StatusNotFound, "Not Found")
		return
	}
	u := s.users[t.login]
	out := map[string]any{
		"scopes": t.scopes, "token": body.AccessToken, "token_last_eight": body.AccessToken[max(0, len(body.AccessToken)-8):],
		"app":        map[string]any{"client_id": id, "name": id, "url": s.publicURL(r)},
		"user":       map[string]any{"id": u.ID, "login": u.Login},
		"created_at": t.created.UTC().Format(time.RFC3339), "updated_at": t.created.UTC().Format(time.RFC3339),
		"expires_at": nil, "note": nil, "fingerprint": nil,
	}
	if !t.expires.IsZero() {
		out["expires_at"] = t.expires.UTC().Format(time.RFC3339)
	}
	writeJSON(w, out)
}
