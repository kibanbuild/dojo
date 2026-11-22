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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kibanbuild/dojo/internal/server"
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

	again := newBrowser(t)
	resp = again.signIn("ada", "", "", false)
	if resp.Header.Get("Location") != "/app" || again.sessionCookie(resp) == nil {
		t.Fatalf("returning: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if code, _ := again.me(); code != http.StatusOK || learnerCount(t, "1001") != "1" {
		t.Fatalf("returning me %d, rows %s", code, learnerCount(t, "1001"))
	}
}

func TestWebSignInUnknownWithoutTicksStoresNothing(t *testing.T) {
	resetLearners(t, "1002")
	b := newBrowser(t)
	resp := b.signIn("bob", "", "age=on", false)
	if resp.Header.Get("Location") != "/start/signin?confirm=1" || b.sessionCookie(resp) != nil {
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
	if resp.Header.Get("Location") != "/start/signin?error=state_check" || b.sessionCookie(resp) != nil {
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
	if resp.Header.Get("Location") != "/start/signin?error=suspended" || b.sessionCookie(resp) != nil {
		t.Fatalf("got %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}
