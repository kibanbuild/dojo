//go:build integration

package server_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kibanyadev/dojo/internal/server"
)

var browserSeq atomic.Int32

// browser is a cookie jar that follows nothing, with its own client IP so no test shares a rate-limit budget.
type browser struct {
	t  *testing.T
	c  *http.Client
	ip string
}

type ipTransport struct{ ip string }

func (i ipTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("X-Forwarded-For", i.ip)
	return http.DefaultTransport.RoundTrip(r)
}

func newBrowser(t *testing.T) *browser {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	n := int(browserSeq.Add(1))
	ip := fmt.Sprintf("10.77.%d.%d", n/250, n%250+1)
	return &browser{t: t, ip: ip, c: &http.Client{Timeout: 15 * time.Second, Jar: jar, Transport: ipTransport{ip},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (b *browser) do(req *http.Request) *http.Response {
	b.t.Helper()
	resp, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp
}

func (b *browser) get(u string) *http.Response {
	b.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	return b.do(req)
}

// signIn runs the whole flow and returns the callback response. tamper changes the state on the way back.
func (b *browser) signIn(login, grant, ticks string, tamper bool) *http.Response {
	b.t.Helper()
	api := apiURL(b.t)
	req, _ := http.NewRequest(http.MethodPost, api+"/v1/auth/github/start", strings.NewReader(ticks))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	start := b.do(req)
	authorize, err := url.Parse(start.Header.Get("Location"))
	if start.StatusCode != http.StatusSeeOther || err != nil || authorize.Path != "/login/oauth/authorize" {
		b.t.Fatalf("start: %d %q", start.StatusCode, start.Header.Get("Location"))
	}
	q := authorize.Query()
	if q.Get("client_id") != "dojo-web-local" || q.Get("code_challenge_method") != "S256" ||
		q.Get("redirect_uri") != "http://localhost:8080/v1/auth/github/callback" {
		b.t.Fatalf("authorize query: %v", q)
	}
	q.Set("login", login)
	if grant != "" {
		q.Set("grant", grant)
	}
	// The browser would open localhost:8081; the test reaches the same fake on its published port.
	back := b.get(os.Getenv("DOJO_FAKE_GITHUB_URL") + authorize.Path + "?" + q.Encode())
	cb, err := url.Parse(back.Header.Get("Location"))
	if err != nil || cb.Query().Get("code") == "" {
		b.t.Fatalf("authorize: %d %q", back.StatusCode, back.Header.Get("Location"))
	}
	cq := cb.Query()
	if tamper {
		cq.Set("state", "tampered")
	}
	return b.get(api + cb.Path + "?" + cq.Encode())
}

func (b *browser) me() (int, map[string]any) {
	b.t.Helper()
	resp, err := b.c.Get(apiURL(b.t) + "/v1/me")
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (b *browser) sessionCookie(resp *http.Response) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == server.SessionCookie {
			return c
		}
	}
	return nil
}

// resetLearners deletes the fixtures by GitHub id (sessions cascade) before and after the test,
// so a rerun or a shuffled order starts from the same state.
func resetLearners(t *testing.T, ids ...string) {
	t.Helper()
	del := "DELETE FROM learners WHERE github_id IN (" + strings.Join(ids, ",") + ")"
	asOwner(t, del)
	t.Cleanup(func() { asOwner(t, del) })
}

func learnerCount(t *testing.T, githubID string) string {
	t.Helper()
	return asOwner(t, "SELECT count(*) FROM learners WHERE github_id = "+githubID)
}

func TestWebSignInNewAndReturningLearner(t *testing.T) {
	resetLearners(t, "1001")
	b := newBrowser(t)
	resp := b.signIn("ada", "", "age=on&terms=on", false)
	c := b.sessionCookie(resp)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/app" || c == nil || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("callback: %d %q %+v", resp.StatusCode, resp.Header.Get("Location"), c)
	}
	code, me := b.me()
	if code != http.StatusOK || me["github_login"] != "ada" || me["username"] != "ada" || me["email"] != "ada@example.test" {
		t.Fatalf("me: %d %v", code, me)
	}
	if got := asOwner(t, "SELECT age_confirmed_at IS NOT NULL AND terms_accepted_at IS NOT NULL FROM learners WHERE github_id = 1001"); got != "t" {
		t.Fatalf("confirmations stored: %q", got)
	}

	if got := asOwner(t, "SELECT terms_version FROM learners WHERE github_id = 1001"); got != envExample(t, "TERMS_VERSION") {
		t.Fatalf("terms version stored: %q", got)
	}
	// A later change of the configured version must not rewrite what this learner accepted.
	asOwner(t, "UPDATE learners SET terms_version = 'accepted-earlier' WHERE github_id = 1001")

	again := newBrowser(t)
	resp = again.signIn("ada", "", "", false)
	if resp.Header.Get("Location") != "/app" || again.sessionCookie(resp) == nil {
		t.Fatalf("returning: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if code, _ := again.me(); code != http.StatusOK || learnerCount(t, "1001") != "1" {
		t.Fatalf("returning me %d, rows %s", code, learnerCount(t, "1001"))
	}
	if got := asOwner(t, "SELECT terms_version FROM learners WHERE github_id = 1001"); got != "accepted-earlier" {
		t.Fatalf("returning sign-in rewrote terms version: %q", got)
	}
}

// GitHub logins of 1 and 39 characters fail the username CHECK as they are; sign-up still works with
// a stable name built from the GitHub id (design 3.1), and a returning sign-in keeps it.
func TestWebSignInLoginsOutsideTheUsernameRule(t *testing.T) {
	for _, tt := range []struct{ login, id, want string }{
		{"z", "1004", "z-" + strconv.FormatInt(1004, 36)},
		{"a-very-long-github-login-of-39-chars-xx", "1005", "a-very-long-github-login-of" + "-" + strconv.FormatInt(1005, 36)},
	} {
		resetLearners(t, tt.id)
		b := newBrowser(t)
		resp := b.signIn(tt.login, "", "age=on&terms=on", false)
		if resp.Header.Get("Location") != "/app" || b.sessionCookie(resp) == nil {
			t.Fatalf("%s: %d %q", tt.login, resp.StatusCode, resp.Header.Get("Location"))
		}
		if code, me := b.me(); code != http.StatusOK || me["github_login"] != tt.login || me["username"] != tt.want {
			t.Fatalf("%s: me %d %v, want username %q", tt.login, code, me, tt.want)
		}
		again := newBrowser(t)
		if resp := again.signIn(tt.login, "", "", false); resp.Header.Get("Location") != "/app" {
			t.Fatalf("%s returning: %q", tt.login, resp.Header.Get("Location"))
		}
		if code, me := again.me(); code != http.StatusOK || me["username"] != tt.want || learnerCount(t, tt.id) != "1" {
			t.Fatalf("%s returning: %d %v", tt.login, code, me)
		}
	}
}

func TestWebSignInUnknownWithoutTicksStoresNothing(t *testing.T) {
	resetLearners(t, "1002")
	b := newBrowser(t)
	resp := b.signIn("bob", "", "age=on", false)
	if resp.Header.Get("Location") != "/app/signin?confirm=1" || b.sessionCookie(resp) != nil {
		t.Fatalf("got %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if n := learnerCount(t, "1002"); n != "0" {
		t.Fatalf("%s rows for an unconfirmed account", n)
	}
	if code, _ := b.me(); code != http.StatusUnauthorized {
		t.Fatalf("me: %d", code)
	}
}

func TestWebSignInTamperedStateGivesNoSession(t *testing.T) {
	resetLearners(t, "1003")
	b := newBrowser(t)
	resp := b.signIn("cy", "", "age=on&terms=on", true)
	if resp.Header.Get("Location") != "/app/signin?error=state_check" || b.sessionCookie(resp) != nil {
		t.Fatalf("got %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if code, _ := b.me(); code != http.StatusUnauthorized || learnerCount(t, "1003") != "0" {
		t.Fatalf("me %d", code)
	}
}

// Without user:email the sign-in still works and stores no email. A successful sign-in also shows the
// fake answered the revoke: the API fails the sign-in when it does not.
func TestWebSignInWithoutEmailGrant(t *testing.T) {
	resetLearners(t, "1003")
	b := newBrowser(t)
	resp := b.signIn("cy", "read:user", "age=on&terms=on", false)
	if resp.Header.Get("Location") != "/app" {
		t.Fatalf("got %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if code, me := b.me(); code != http.StatusOK || me["email"] != nil {
		t.Fatalf("me: %d %v", code, me)
	}
	// No address, so no welcome mail is queued (a later prompt asks for one).
	if got := asOwner(t, "SELECT count(*) FROM email_outbox WHERE ref IN (SELECT id::text FROM learners WHERE github_id = 1003)"); got != "0" {
		t.Fatalf("queued mail for a learner with no address: %s", got)
	}
}

func TestWebSignInSuspendedLearnerGetsNoSession(t *testing.T) {
	resetLearners(t, "1002")
	setup := newBrowser(t)
	if resp := setup.signIn("bob", "", "age=on&terms=on", false); resp.Header.Get("Location") != "/app" {
		t.Fatalf("setup: %q", resp.Header.Get("Location"))
	}
	asOwner(t, "UPDATE learners SET suspended_at = now() WHERE github_id = 1002")
	b := newBrowser(t)
	resp := b.signIn("bob", "", "", false)
	if resp.Header.Get("Location") != "/app/signin?error=suspended" || b.sessionCookie(resp) != nil {
		t.Fatalf("got %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func (b *browser) logout(csrf bool) *http.Response {
	b.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, apiURL(b.t)+"/v1/auth/logout", nil)
	if csrf {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(server.CSRFHeader, "1")
	}
	return b.do(req)
}

func TestWebSignOutRevokesTheSession(t *testing.T) {
	resetLearners(t, "1003")
	b := newBrowser(t)
	if resp := b.signIn("cy", "", "age=on&terms=on", false); resp.Header.Get("Location") != "/app" {
		t.Fatalf("sign-in: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	token := ""
	u, _ := url.Parse(apiURL(t))
	for _, c := range b.c.Jar.Cookies(u) {
		if c.Name == server.SessionCookie {
			token = c.Value
		}
	}
	if code, _ := b.me(); code != http.StatusOK || token == "" {
		t.Fatalf("signed in: me %d, token %q", code, token)
	}

	if resp := b.logout(false); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("without CSRF: %d", resp.StatusCode)
	}
	if code, _ := b.me(); code != http.StatusOK {
		t.Fatalf("a refused sign-out ended the session: me %d", code)
	}

	resp := b.logout(true)
	c := b.sessionCookie(resp)
	if resp.StatusCode != http.StatusNoContent || c == nil || c.Value != "" || c.MaxAge != -1 || c.Path != "/" || !c.HttpOnly {
		t.Fatalf("logout: %d %+v", resp.StatusCode, c)
	}
	if got := asOwner(t, "SELECT count(*) FROM sessions s JOIN learners l ON l.id = s.learner_id WHERE l.github_id = 1003 AND s.revoked_at IS NOT NULL"); got != "1" {
		t.Fatalf("revoked sessions: %s", got)
	}
	if code, _ := b.me(); code != http.StatusUnauthorized {
		t.Fatalf("after sign-out me: %d", code)
	}
	// The old cookie value is dead on the server, not only dropped by the browser.
	if code, _ := getMe(t, token); code != http.StatusUnauthorized {
		t.Fatalf("replayed cookie: %d", code)
	}
	if resp := b.logout(true); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("second logout: %d", resp.StatusCode)
	}
}
