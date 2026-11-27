//go:build integration

package admin_test

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/kibanyadev/dojo/internal/admin"
)

func (b *operatorBrowser) postForm(path string, form url.Values, hdr map[string]string) *http.Response {
	b.t.Helper()
	req, err := http.NewRequest(http.MethodPost, adminURL(b.t)+path, strings.NewReader(form.Encode()))
	if err != nil {
		b.t.Fatal(err)
	}
	req.Host = admin.Host
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp
}

var csrfInput = regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)

func (b *operatorBrowser) csrf() string {
	b.t.Helper()
	_, body := b.admin(http.MethodGet, "/")
	m := csrfInput.FindStringSubmatch(body)
	if m == nil {
		b.t.Fatalf("no csrf_token on the landing page: %s", body)
	}
	return m[1]
}

func TestSignoutNeedsTheCSRFToken(t *testing.T) {
	seedOperator(t, "1001", "ada", false)
	b := newOperatorBrowser(t)
	if sessionCookie(b.signIn("ada")) == nil {
		t.Fatal("no session")
	}
	tok := b.csrf()
	alive := func() {
		t.Helper()
		if resp, _ := b.admin(http.MethodGet, "/"); resp.StatusCode != 200 {
			t.Fatalf("session ended: %d", resp.StatusCode)
		}
	}

	// A forced sign-out from another page: no token, a wrong token, a cross-origin post with the token.
	for name, c := range map[string]struct {
		form url.Values
		hdr  map[string]string
	}{
		"no token":     {nil, nil},
		"wrong token":  {url.Values{"csrf_token": {"nope"}}, nil},
		"other origin": {url.Values{"csrf_token": {tok}}, map[string]string{"Origin": "http://127.0.0.1:9999"}},
		"cross-site":   {url.Values{"csrf_token": {tok}}, map[string]string{"Sec-Fetch-Site": "cross-site"}},
		"null origin":  {url.Values{"csrf_token": {tok}}, map[string]string{"Origin": "null"}},
	} {
		if resp := b.postForm("/signout", c.form, c.hdr); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s: %d", name, resp.StatusCode)
		}
		alive()
	}
	if got := psqlAsOwner(t, "SELECT count(*) FROM admin_sessions WHERE revoked_at IS NOT NULL"); got != "0" {
		t.Fatalf("revoked sessions after refused posts: %s", got)
	}

	// What Chrome sends for a same-origin form post under Referrer-Policy: no-referrer.
	resp := b.postForm("/signout", url.Values{"csrf_token": {tok}}, map[string]string{"Origin": "null", "Sec-Fetch-Site": "same-origin"})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/signin" {
		t.Fatalf("signout with token: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp, _ := b.admin(http.MethodGet, "/"); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("after signout: %d", resp.StatusCode)
	}
	if got := psqlAsOwner(t, "SELECT count(*) FROM admin_sessions WHERE revoked_at IS NOT NULL"); got != "1" {
		t.Fatalf("revoked sessions: %s", got)
	}
}

// Revoking the operator or the one session in the database locks out the very next request.
func TestRevocationLocksOutTheNextRequest(t *testing.T) {
	for name, revoke := range map[string]string{
		"operator": "UPDATE operators SET revoked_at = now() WHERE github_id = 1001",
		"session":  "UPDATE admin_sessions SET revoked_at = now()",
	} {
		t.Run(name, func(t *testing.T) {
			seedOperator(t, "1001", "ada", false)
			b := newOperatorBrowser(t)
			if sessionCookie(b.signIn("ada")) == nil {
				t.Fatal("no session")
			}
			tok := b.csrf()
			if resp, _ := b.admin(http.MethodGet, "/"); resp.StatusCode != 200 {
				t.Fatalf("before: %d", resp.StatusCode)
			}
			psqlAsOwner(t, revoke)
			if resp, _ := b.admin(http.MethodGet, "/"); resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/signin" {
				t.Fatalf("next GET: %d %q", resp.StatusCode, resp.Header.Get("Location"))
			}
			// The session cookie was cleared, so a valid token alone gets nowhere.
			if resp := b.postForm("/signout", url.Values{"csrf_token": {tok}}, nil); resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/signin" {
				t.Fatalf("next POST: %d %q", resp.StatusCode, resp.Header.Get("Location"))
			}
		})
	}
}
