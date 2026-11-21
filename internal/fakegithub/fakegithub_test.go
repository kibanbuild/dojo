package fakegithub_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/kibanbuild/dojo/internal/fakegithub"
)

const (
	cbURL    = "http://localhost:8080/cb"
	verifier = "a-long-enough-random-code-verifier-0123456789"
)

var challenge = func() string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}()

type env struct {
	t   *testing.T
	srv *httptest.Server
	now time.Time
}

func newEnv(t *testing.T) *env {
	e := &env{t: t, now: time.Unix(1_800_000_000, 0)}
	h := fakegithub.New(fakegithub.Config{
		Clients: []fakegithub.Client{{ID: "cid", Secret: "sec", RedirectURIs: []string{cbURL}}, {ID: "other", Secret: "s2", RedirectURIs: []string{cbURL}}},
		Users:   fakegithub.DefaultUsers(),
		Now:     func() time.Time { return e.now },
	})
	e.srv = httptest.NewServer(h)
	t.Cleanup(e.srv.Close)
	return e
}

func (e *env) client() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// authorize returns the callback query the browser would land on.
func (e *env) authorize(q url.Values) url.Values {
	e.t.Helper()
	resp, err := e.client().Get(e.srv.URL + "/login/oauth/authorize?" + q.Encode())
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		e.t.Fatalf("authorize: got %d", resp.StatusCode)
	}
	loc, _ := url.Parse(resp.Header.Get("Location"))
	return loc.Query()
}

func authQuery(login, scope string) url.Values {
	return url.Values{"client_id": {"cid"}, "redirect_uri": {cbURL}, "scope": {scope}, "state": {"st"},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"}, "login": {login}}
}

func (e *env) exchange(code, ver string) map[string]string {
	e.t.Helper()
	form := url.Values{"client_id": {"cid"}, "client_secret": {"sec"}, "code": {code}, "code_verifier": {ver}}
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/login/oauth/access_token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		e.t.Fatalf("access_token: got %d, GitHub answers 200 even on errors", resp.StatusCode)
	}
	var out map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		e.t.Fatal(err)
	}
	return out
}

// signIn runs the whole web flow and returns the access token.
func (e *env) signIn(login, scope string) string {
	e.t.Helper()
	cb := e.authorize(authQuery(login, scope))
	out := e.exchange(cb.Get("code"), verifier)
	if out["access_token"] == "" {
		e.t.Fatalf("no token: %v", out)
	}
	return out["access_token"]
}

type reply struct {
	StatusCode int
	Header     http.Header
}

func (e *env) get(path, tok string) (reply, map[string]any) {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+path, nil)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return reply{resp.StatusCode, resp.Header}, out
}

func TestWebFlowEndToEnd(t *testing.T) {
	e := newEnv(t)
	cb := e.authorize(authQuery("ada", "read:user user:email"))
	if cb.Get("state") != "st" || cb.Get("code") == "" {
		t.Fatalf("callback %v", cb)
	}
	out := e.exchange(cb.Get("code"), verifier)
	if out["token_type"] != "bearer" || out["scope"] != "read:user,user:email" {
		t.Fatalf("got %v", out)
	}
	resp, u := e.get("/user", out["access_token"])
	if resp.StatusCode != 200 || u["login"] != "ada" || u["two_factor_authentication"] != true || u["id"].(float64) != 1001 {
		t.Fatalf("got %d %v", resp.StatusCode, u)
	}
	if got := resp.Header.Get("X-OAuth-Scopes"); got != "read:user, user:email" {
		t.Fatalf("X-OAuth-Scopes %q", got)
	}
}

func TestAuthorizeRejectsPlainAndMissingMethod(t *testing.T) {
	for _, method := range []string{"plain", ""} {
		e := newEnv(t)
		q := authQuery("ada", "read:user")
		if method == "" {
			q.Del("code_challenge_method")
		} else {
			q.Set("code_challenge_method", method)
		}
		cb := e.authorize(q)
		if cb.Get("error") != "invalid_request" || cb.Get("code") != "" || cb.Get("state") != "st" {
			t.Errorf("method %q: callback %v", method, cb)
		}
	}
}

func TestAuthorizeValidatesClientAndRedirect(t *testing.T) {
	e := newEnv(t)
	for name, mod := range map[string]func(url.Values){
		"client":   func(q url.Values) { q.Set("client_id", "nope") },
		"redirect": func(q url.Values) { q.Set("redirect_uri", "http://evil.test/cb") },
	} {
		q := authQuery("ada", "read:user")
		mod(q)
		resp, err := e.client().Get(e.srv.URL + "/login/oauth/authorize?" + q.Encode())
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound || resp.Header.Get("Location") != "" {
			t.Errorf("%s: got %d", name, resp.StatusCode)
		}
	}
}

func TestAuthorizeShowsUserChooserWithoutLogin(t *testing.T) {
	e := newEnv(t)
	q := authQuery("", "read:user")
	q.Del("login")
	resp, err := e.client().Get(e.srv.URL + "/login/oauth/authorize?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := readAll(resp.Body)
	for _, want := range []string{`value="ada"`, `value="bob"`, `value="cy"`, `name="state" value="st"`} {
		if resp.StatusCode != 200 || !strings.Contains(buf, want) {
			t.Fatalf("missing %s in %d %s", want, resp.StatusCode, buf)
		}
	}
}

func TestPKCE(t *testing.T) {
	e := newEnv(t)
	cb := e.authorize(authQuery("ada", "read:user"))
	if out := e.exchange(cb.Get("code"), "the-wrong-verifier"); out["error"] != "bad_verification_code" {
		t.Fatalf("wrong verifier: %v", out)
	}
	if out := e.exchange(cb.Get("code"), verifier); out["error"] != "bad_verification_code" {
		t.Fatalf("a failed exchange must burn the code: %v", out)
	}
}

func TestCodeIsSingleUse(t *testing.T) {
	e := newEnv(t)
	cb := e.authorize(authQuery("ada", "read:user"))
	if out := e.exchange(cb.Get("code"), verifier); out["access_token"] == "" {
		t.Fatalf("first use: %v", out)
	}
	if out := e.exchange(cb.Get("code"), verifier); out["error"] != "bad_verification_code" {
		t.Fatalf("second use: %v", out)
	}
}

func TestCodeExpires(t *testing.T) {
	e := newEnv(t)
	cb := e.authorize(authQuery("ada", "read:user"))
	e.now = e.now.Add(11 * time.Minute)
	if out := e.exchange(cb.Get("code"), verifier); out["error"] != "bad_verification_code" {
		t.Fatalf("got %v", out)
	}
}

func TestTokenEndpointChecksTheSecret(t *testing.T) {
	e := newEnv(t)
	cb := e.authorize(authQuery("ada", "read:user"))
	form := url.Values{"client_id": {"cid"}, "client_secret": {"wrong"}, "code": {cb.Get("code")}, "code_verifier": {verifier}}
	resp, err := http.PostForm(e.srv.URL+"/login/oauth/access_token", form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := readAll(resp.Body)
	got, _ := url.ParseQuery(buf)
	if resp.StatusCode != 200 || got.Get("error") != "incorrect_client_credentials" ||
		!strings.HasPrefix(resp.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		t.Fatalf("got %d %q %v", resp.StatusCode, resp.Header.Get("Content-Type"), got)
	}
}

func TestCodeBelongsToItsClient(t *testing.T) {
	e := newEnv(t)
	cb := e.authorize(authQuery("ada", "read:user"))
	form := url.Values{"client_id": {"other"}, "client_secret": {"s2"}, "code": {cb.Get("code")}, "code_verifier": {verifier}}
	resp, err := http.PostForm(e.srv.URL+"/login/oauth/access_token", form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := readAll(resp.Body)
	if !strings.Contains(buf, "error=bad_verification_code") {
		t.Fatalf("got %s", buf)
	}
}

func TestTwoFactorFieldIsScopeGated(t *testing.T) {
	cases := []struct {
		scope   string
		present bool
	}{{"read:user", true}, {"user", true}, {"user:email", false}, {"", false}}
	for _, c := range cases {
		e := newEnv(t)
		_, u := e.get("/user", e.signIn("bob", c.scope))
		v, present := u["two_factor_authentication"]
		if present != c.present || (present && v != false) {
			t.Errorf("scope %q: got %v present=%v", c.scope, v, present)
		}
	}
}

func TestUserTrimmedScopes(t *testing.T) {
	e := newEnv(t)
	q := authQuery("ada", "read:user user:email")
	q.Set("grant", "user:email")
	out := e.exchange(e.authorize(q).Get("code"), verifier)
	if out["scope"] != "user:email" {
		t.Fatalf("got %v", out)
	}
	resp, u := e.get("/user", out["access_token"])
	if _, present := u["two_factor_authentication"]; present || resp.Header.Get("X-OAuth-Scopes") != "user:email" {
		t.Fatalf("trimmed scope still shows the field: %v %q", u, resp.Header.Get("X-OAuth-Scopes"))
	}
}

func TestSeededUsers(t *testing.T) {
	e := newEnv(t)
	_, ada := e.get("/user", e.signIn("ada", "read:user"))
	_, bob := e.get("/user", e.signIn("bob", "read:user"))
	_, cy := e.get("/user", e.signIn("cy", "read:user"))
	if ada["two_factor_authentication"] != true || bob["two_factor_authentication"] != false {
		t.Fatalf("2FA: ada %v bob %v", ada, bob)
	}
	if cy["email"] != nil {
		t.Fatalf("cy has a public email: %v", cy)
	}
}

func TestEmails(t *testing.T) {
	e := newEnv(t)
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/user/emails", nil)
	req.Header.Set("Authorization", "token "+e.signIn("ada", "user:email"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var list []fakegithub.Email
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil || len(list) != 1 || !list[0].Primary || !list[0].Verified {
		t.Fatalf("got %v %v", list, err)
	}
	if resp, _ := e.get("/user/emails", e.signIn("ada", "read:user")); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("without user:email: got %d", resp.StatusCode)
	}
	if _, cy := e.get("/user/emails", e.signIn("cy", "user:email")); cy != nil {
		t.Fatalf("expected a JSON array, got object %v", cy)
	}
}

func TestBadTokenIs401(t *testing.T) {
	e := newEnv(t)
	for _, tok := range []string{"", "gho_nope"} {
		resp, body := e.get("/user", tok)
		if resp.StatusCode != http.StatusUnauthorized || body["message"] != "Bad credentials" {
			t.Errorf("token %q: got %d %v", tok, resp.StatusCode, body)
		}
	}
}

func (e *env) revoke(client, secret, path, body string) int {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, e.srv.URL+"/applications/"+path+"/token", strings.NewReader(body))
	req.SetBasicAuth(client, secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestRevoke(t *testing.T) {
	e := newEnv(t)
	tok := e.signIn("ada", "read:user")
	body := `{"access_token":"` + tok + `"}`
	if got := e.revoke("cid", "bad", "cid", body); got != 401 {
		t.Fatalf("bad secret: %d", got)
	}
	if got := e.revoke("other", "s2", "other", body); got != 404 {
		t.Fatalf("another client's token: %d", got)
	}
	if got := e.revoke("cid", "sec", "cid", `{}`); got != 422 {
		t.Fatalf("missing token: %d", got)
	}
	if got := e.revoke("cid", "sec", "cid", body); got != 204 {
		t.Fatalf("revoke: %d", got)
	}
	if resp, _ := e.get("/user", tok); resp.StatusCode != 401 {
		t.Fatalf("after revoke: %d", resp.StatusCode)
	}
	if got := e.revoke("cid", "sec", "cid", body); got != 404 {
		t.Fatalf("revoke twice: %d", got)
	}
}

// The fake must never ship inside dojo-api.
func TestAPIBinaryDoesNotImportTheFake(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "github.com/kibanbuild/dojo/cmd/dojo-api").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	if strings.Contains(string(out), "/internal/fakegithub") {
		t.Fatal("dojo-api imports internal/fakegithub")
	}
}

func readAll(r io.Reader) string {
	b, _ := io.ReadAll(r)
	return string(b)
}
