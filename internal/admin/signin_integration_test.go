//go:build integration

package admin_test

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/kibanbuild/dojo/internal/admin"
)

// psqlAsOwner runs sql in the db container as the migration role, which owns the tables. The
// bootstrap command that seeds the first operator is 12e; here the test seeds the row itself.
func psqlAsOwner(t *testing.T, sql string) string {
	t.Helper()
	project := os.Getenv("DOJO_COMPOSE_PROJECT")
	if project == "" {
		t.Fatal("DOJO_COMPOSE_PROJECT is not set: run `make test-integration`")
	}
	ids, err := exec.Command("docker", "ps", "-q",
		"--filter", "label=com.docker.compose.project="+project,
		"--filter", "label=com.docker.compose.service=db").Output()
	id := strings.TrimSpace(string(ids))
	if err != nil || id == "" {
		t.Fatalf("find db container: %v %q", err, ids)
	}
	out, err := exec.Command("docker", "exec", "-e", "PGPASSWORD="+envExample(t, "POSTGRES_PASSWORD"), id,
		"psql", "-h", id, "-U", "dojo", "-d", "dojo", "-v", "ON_ERROR_STOP=1", "-tA", "-c", sql).CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v %s", sql, err, out)
	}
	return strings.TrimSpace(string(out))
}

func envExample(t *testing.T, key string) string {
	t.Helper()
	b, err := os.ReadFile("../../.env.example")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, key+"="); ok {
			return v
		}
	}
	t.Fatalf("%s not in .env.example", key)
	return ""
}

// seedOperator starts the test from no operators and no sessions of the fixture ids, and cleans up.
func seedOperator(t *testing.T, githubID, login string, revoked bool) {
	t.Helper()
	reset := "DELETE FROM admin_audit WHERE operator_id IN (SELECT id FROM operators WHERE github_id IN (1001, 1002)); " +
		"DELETE FROM admin_sessions WHERE operator_id IN (SELECT id FROM operators WHERE github_id IN (1001, 1002)); " +
		"DELETE FROM operators WHERE github_id IN (1001, 1002)"
	psqlAsOwner(t, reset)
	t.Cleanup(func() { psqlAsOwner(t, reset) })
	if githubID == "" {
		return
	}
	sql := "INSERT INTO operators (github_id, github_login" + map[bool]string{true: ", revoked_at", false: ""}[revoked] +
		") VALUES (" + githubID + ", '" + login + "'" + map[bool]string{true: ", now()", false: ""}[revoked] + ")"
	psqlAsOwner(t, sql)
}

// operatorBrowser keeps cookies and follows no redirect, and sends the one Host the service answers.
type operatorBrowser struct {
	t *testing.T
	c *http.Client
}

func newOperatorBrowser(t *testing.T) *operatorBrowser {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	return &operatorBrowser{t: t, c: &http.Client{Timeout: 15 * time.Second, Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (b *operatorBrowser) do(method, target string, host string) (*http.Response, string) {
	b.t.Helper()
	req, err := http.NewRequest(method, target, nil)
	if err != nil {
		b.t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
	resp, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

func (b *operatorBrowser) admin(method, path string) (*http.Response, string) {
	return b.do(method, adminURL(b.t)+path, admin.Host)
}

// signIn runs the whole flow as login and returns the callback response.
func (b *operatorBrowser) signIn(login string) *http.Response {
	b.t.Helper()
	start, _ := b.admin(http.MethodPost, "/auth/github/start")
	authorize, err := url.Parse(start.Header.Get("Location"))
	if start.StatusCode != http.StatusSeeOther || err != nil || authorize.Path != "/login/oauth/authorize" {
		b.t.Fatalf("start: %d %q", start.StatusCode, start.Header.Get("Location"))
	}
	q := authorize.Query()
	if q.Get("client_id") != "dojo-admin-local" || q.Get("scope") != "read:user" ||
		q.Get("redirect_uri") != "http://127.0.0.1:8090/auth/github/callback" {
		b.t.Fatalf("authorize query: %v", q)
	}
	q.Set("login", login)
	// The browser would open localhost:8081; the test reaches the same fake on its published port.
	back, _ := b.do(http.MethodGet, os.Getenv("DOJO_FAKE_GITHUB_URL")+authorize.Path+"?"+q.Encode(), "")
	cb, err := url.Parse(back.Header.Get("Location"))
	if err != nil || cb.Path != "/auth/github/callback" || cb.Query().Get("code") == "" {
		b.t.Fatalf("authorize: %d %q", back.StatusCode, back.Header.Get("Location"))
	}
	resp, _ := b.admin(http.MethodGet, cb.Path+"?"+cb.RawQuery)
	return resp
}

func sessionCookie(resp *http.Response) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == "dojo_admin_session" {
			return c
		}
	}
	return nil
}

func TestOperatorSignInEndToEnd(t *testing.T) {
	seedOperator(t, "1001", "ada", false)
	b := newOperatorBrowser(t)

	if resp, _ := b.admin(http.MethodGet, "/"); resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/signin" {
		t.Fatalf("anonymous landing: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp, body := b.admin(http.MethodGet, "/signin"); resp.StatusCode != 200 || !strings.Contains(body, "Sign in with GitHub") {
		t.Fatalf("signin page: %d %s", resp.StatusCode, body)
	}

	resp := b.signIn("ada")
	c := sessionCookie(resp)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" || c == nil ||
		!c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Secure || c.Domain != "" || c.MaxAge != 3600 {
		t.Fatalf("callback: %d %q %+v", resp.StatusCode, resp.Header.Get("Location"), c)
	}
	if resp, body := b.admin(http.MethodGet, "/"); resp.StatusCode != 200 || !strings.Contains(body, "Signed in as ada") {
		t.Fatalf("landing: %d %s", resp.StatusCode, body)
	}

	// Stored as a hash, one hour fixed.
	if got := psqlAsOwner(t, "SELECT count(*) FROM admin_sessions s JOIN operators o ON o.id = s.operator_id "+
		"WHERE o.github_id = 1001 AND octet_length(s.token_hash) = 32 AND s.expires_at = s.created_at + interval '1 hour' AND s.revoked_at IS NULL"); got != "1" {
		t.Fatalf("stored sessions: %s", got)
	}

	// Sign out ends it.
	if resp := b.postForm("/signout", url.Values{"csrf_token": {b.csrf()}}, nil); resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/signin" {
		t.Fatalf("signout: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp, _ := b.admin(http.MethodGet, "/"); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("landing after signout: %d", resp.StatusCode)
	}
	if got := psqlAsOwner(t, "SELECT count(*) FROM admin_sessions WHERE revoked_at IS NOT NULL"); got != "1" {
		t.Fatalf("revoked sessions: %s", got)
	}
}

func TestOperatorSignInRefusals(t *testing.T) {
	tests := []struct {
		name    string
		seed    func(t *testing.T)
		login   string
		wantErr string
	}{
		{"not an operator", func(t *testing.T) { seedOperator(t, "", "", false) }, "ada", "not_authorized"},
		{"revoked operator", func(t *testing.T) { seedOperator(t, "1001", "ada", true) }, "ada", "not_authorized"},
		{"operator without 2fa", func(t *testing.T) { seedOperator(t, "1002", "bob", false) }, "bob", "two_factor"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.seed(t)
			b := newOperatorBrowser(t)
			resp := b.signIn(tt.login)
			if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/signin?error="+tt.wantErr || sessionCookie(resp) != nil {
				t.Fatalf("callback: %d %q %v", resp.StatusCode, resp.Header.Get("Location"), sessionCookie(resp))
			}
			if got := psqlAsOwner(t, "SELECT count(*) FROM admin_sessions"); got != "0" {
				t.Fatalf("sessions: %s", got)
			}
			if resp, _ := b.admin(http.MethodGet, "/"); resp.StatusCode != http.StatusSeeOther {
				t.Fatalf("landing: %d", resp.StatusCode)
			}
		})
	}
}

func TestOperatorStateIsOneUseAndBoundToTheBrowser(t *testing.T) {
	seedOperator(t, "1001", "ada", false)
	victim := newOperatorBrowser(t)
	start, _ := victim.admin(http.MethodPost, "/auth/github/start")
	authorize, _ := url.Parse(start.Header.Get("Location"))
	q := authorize.Query()
	q.Set("login", "ada")
	back, _ := victim.do(http.MethodGet, os.Getenv("DOJO_FAKE_GITHUB_URL")+authorize.Path+"?"+q.Encode(), "")
	cb, _ := url.Parse(back.Header.Get("Location"))

	// Another browser with no pre-session cookie cannot finish the flow.
	other := newOperatorBrowser(t)
	if resp, _ := other.admin(http.MethodGet, cb.Path+"?"+cb.RawQuery); resp.Header.Get("Location") != "/signin?error=state_check" {
		t.Fatalf("other browser: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp, _ := victim.admin(http.MethodGet, cb.Path+"?"+cb.RawQuery); resp.Header.Get("Location") != "/" {
		t.Fatalf("first use: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	// The same callback replayed is refused, even with the cookie jar's session in place.
	if resp, _ := newOperatorBrowser(t).admin(http.MethodGet, cb.Path+"?"+cb.RawQuery); resp.Header.Get("Location") != "/signin?error=state_check" {
		t.Fatalf("replay: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestOperatorSessionExpiresAndDiesWithTheOperator(t *testing.T) {
	seedOperator(t, "1001", "ada", false)
	b := newOperatorBrowser(t)
	if resp := b.signIn("ada"); sessionCookie(resp) == nil {
		t.Fatalf("no session: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp, _ := b.admin(http.MethodGet, "/"); resp.StatusCode != 200 {
		t.Fatalf("landing: %d", resp.StatusCode)
	}

	// Two hours back: the CHECK pins expires_at to created_at + 1 hour, so both move together.
	psqlAsOwner(t, "UPDATE admin_sessions SET created_at = created_at - interval '2 hours', expires_at = expires_at - interval '2 hours'")
	if resp, _ := b.admin(http.MethodGet, "/"); resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/signin" {
		t.Fatalf("expired session: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}

	// A fresh session ends when its operator is revoked.
	b = newOperatorBrowser(t)
	if resp := b.signIn("ada"); sessionCookie(resp) == nil {
		t.Fatalf("no second session: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	psqlAsOwner(t, "UPDATE operators SET revoked_at = now() WHERE github_id = 1001")
	if resp, _ := b.admin(http.MethodGet, "/"); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("revoked operator: %d", resp.StatusCode)
	}
}
