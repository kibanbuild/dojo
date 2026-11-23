package github

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func testClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return New(Config{ClientID: "cid", ClientSecret: "sek", AuthorizeURL: "http://browser.test", OAuthURL: s.URL, APIURL: s.URL})
}

func TestNewPKCE(t *testing.T) {
	v, c, err := NewPKCE()
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(v))
	if len(v) != 43 || c != base64.RawURLEncoding.EncodeToString(sum[:]) {
		t.Fatalf("verifier %q challenge %q", v, c)
	}
	if v2, _, _ := NewPKCE(); v2 == v {
		t.Fatal("verifier repeated")
	}
}

func TestAuthorizeURL(t *testing.T) {
	c := New(Config{ClientID: "cid", AuthorizeURL: "http://browser.test"})
	u, err := url.Parse(c.AuthorizeURL("st", "ch", "http://app/cb"))
	if err != nil || u.Host != "browser.test" || u.Path != "/login/oauth/authorize" {
		t.Fatalf("%v %v", u, err)
	}
	want := map[string]string{"client_id": "cid", "redirect_uri": "http://app/cb", "scope": "read:user user:email",
		"state": "st", "code_challenge": "ch", "code_challenge_method": "S256"}
	for k, v := range want {
		if got := u.Query().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

func ptr[T any](v T) *T { return &v }

func TestAuthorizeURLScopesOverride(t *testing.T) {
	c := New(Config{ClientID: "cid", AuthorizeURL: "http://browser.test", Scopes: "read:user"})
	u, err := url.Parse(c.AuthorizeURL("st", "ch", "http://app/cb"))
	if err != nil || u.Query().Get("scope") != "read:user" {
		t.Fatalf("%v %v", u, err)
	}
}

func TestExchange(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   error
		scopes []string
	}{
		{"ok", 200, `{"access_token":"gho_x","scope":"read:user,user:email","token_type":"bearer"}`, nil, []string{"read:user", "user:email"}},
		{"ok spaced scopes", 200, `{"access_token":"gho_x","scope":"read:user, user:email"}`, nil, []string{"read:user", "user:email"}},
		{"ok empty scope", 200, `{"access_token":"gho_x","scope":""}`, nil, []string{}},
		{"200 with bad code", 200, `{"error":"bad_verification_code"}`, ErrRejected, nil},
		{"200 with denied", 200, `{"error":"access_denied"}`, ErrRejected, nil},
		{"200 with bad credentials", 200, `{"error":"incorrect_client_credentials"}`, ErrUnavailable, nil},
		{"200 no token", 200, `{}`, ErrUnavailable, nil},
		{"500", 500, `oops`, ErrUnavailable, nil},
		{"bad json", 200, `<html>`, ErrUnavailable, nil},
		{"oversized", 200, `{"access_token":"` + strings.Repeat("a", maxBody) + `"}`, ErrUnavailable, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				_ = r.ParseForm()
				if r.URL.RawQuery != "" {
					t.Errorf("exchange put data in the URL: %q", r.URL.RawQuery)
				}
				if r.URL.Path != "/login/oauth/access_token" || r.Method != http.MethodPost || r.Header.Get("Accept") != "application/json" ||
					r.PostForm.Get("code") != "c0de" || r.PostForm.Get("code_verifier") != "ver" ||
					r.PostForm.Get("redirect_uri") != "http://app/cb" || r.PostForm.Get("client_secret") != "sek" {
					t.Errorf("bad request: %s %s %v", r.Method, r.URL.Path, r.PostForm)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})
			tok, err := c.Exchange(context.Background(), "c0de", "ver", "http://app/cb")
			if tt.want == nil {
				if err != nil || tok.AccessToken != "gho_x" || !reflect.DeepEqual(tok.Scopes, tt.scopes) {
					t.Fatalf("%+v %v", tok, err)
				}
				return
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if errors.Is(err, ErrRejected) == errors.Is(err, ErrUnavailable) {
				t.Fatalf("not exactly one kind: %v", err)
			}
		})
	}
}

func TestExchangeUnreachable(t *testing.T) {
	s := httptest.NewServer(http.NotFoundHandler())
	s.Close()
	c := New(Config{ClientSecret: "sek", OAuthURL: s.URL})
	if _, err := c.Exchange(context.Background(), "c", "v", "r"); !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "sek") {
		t.Fatalf("%v", err)
	}
}

func TestCallsDoNotFollowRedirects(t *testing.T) {
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("redirect followed to %s %s with Authorization %q", r.Method, r.URL, r.Header.Get("Authorization"))
	}))
	t.Cleanup(trap.Close)
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, trap.URL+"/stolen", http.StatusTemporaryRedirect)
	})
	ctx := context.Background()
	calls := map[string]func() error{
		"exchange": func() error { _, err := c.Exchange(ctx, "c", "v", "r"); return err },
		"user":     func() error { _, err := c.User(ctx, "tok"); return err },
		"emails":   func() error { _, err := c.Emails(ctx, "tok"); return err },
		"revoke":   func() error { return c.Revoke(ctx, "tok") },
	}
	for name, call := range calls {
		if err := call(); !errors.Is(err, ErrUnavailable) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestUser(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		scopes string
		want   error
		user   User
	}{
		{"ok", 200, `{"id":7,"login":"ada","name":"Ada","email":"a@x.test"}`, "read:user, user:email", nil,
			User{7, "ada", "Ada", "a@x.test", []string{"read:user", "user:email"}, nil}},
		{"null name and email", 200, `{"id":8,"login":"cy","name":null,"email":null}`, "read:user,user:email", nil,
			User{8, "cy", "", "", []string{"read:user", "user:email"}, nil}},
		{"2fa on", 200, `{"id":9,"login":"d","two_factor_authentication":true}`, "read:user", nil,
			User{9, "d", "", "", []string{"read:user"}, ptr(true)}},
		{"2fa off", 200, `{"id":9,"login":"d","two_factor_authentication":false}`, "read:user", nil,
			User{9, "d", "", "", []string{"read:user"}, ptr(false)}},
		{"2fa null", 200, `{"id":9,"login":"d","two_factor_authentication":null}`, "read:user", nil,
			User{9, "d", "", "", []string{"read:user"}, nil}},
		{"bad token", 401, `{"message":"Bad credentials"}`, "", ErrRejected, User{}},
		{"rate limited", 403, `{}`, "", ErrUnavailable, User{}},
		{"server error", 502, ``, "", ErrUnavailable, User{}},
		{"no id", 200, `{"login":"x"}`, "", ErrUnavailable, User{}},
		{"oversized", 200, `{"login":"` + strings.Repeat("a", maxBody) + `"}`, "", ErrUnavailable, User{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/user" || r.Header.Get("Authorization") != "Bearer tok" ||
					r.Header.Get("Accept") != "application/vnd.github+json" || r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" ||
					r.Header.Get("User-Agent") == "" {
					t.Errorf("bad headers: %v", r.Header)
				}
				w.Header().Set("X-OAuth-Scopes", tt.scopes)
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})
			u, err := c.User(context.Background(), "tok")
			if tt.want != nil {
				if !errors.Is(err, tt.want) {
					t.Fatalf("err = %v, want %v", err, tt.want)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(u, tt.user) {
				t.Fatalf("%+v %v", u, err)
			}
		})
	}
}

func TestEmails(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   string
		err    error
	}{
		{"primary verified", 200, `[{"email":"o@x.test","primary":false,"verified":true},{"email":"p@x.test","primary":true,"verified":true}]`, "p@x.test", nil},
		{"primary unverified is never used", 200, `[{"email":"p@x.test","primary":true,"verified":false},{"email":"o@x.test","primary":false,"verified":true}]`, "", nil},
		{"none", 200, `[]`, "", nil},
		{"scope missing", 404, `{"message":"Not Found"}`, "", ErrRejected},
		{"bad token", 401, `{}`, "", ErrRejected},
		{"unavailable", 503, ``, "", ErrUnavailable},
		{"bad json", 200, `{`, "", ErrUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/user/emails" {
					t.Errorf("path %s", r.URL.Path)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})
			got, err := c.Emails(context.Background(), "tok")
			if !errors.Is(err, tt.err) && tt.err != nil || tt.err == nil && err != nil || got != tt.want {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
}

func TestRevoke(t *testing.T) {
	for _, tt := range []struct {
		status int
		ok     bool
	}{{204, true}, {404, true}, {200, false}, {401, false}, {422, false}, {500, false}} {
		t.Run(fmt.Sprint(tt.status), func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				id, sec, ok := r.BasicAuth()
				b := new(strings.Builder)
				_, _ = fmt.Fprint(b, r.Method, " ", r.URL.Path)
				if b.String() != "DELETE /applications/cid/token" || !ok || id != "cid" || sec != "sek" {
					t.Errorf("bad request %s basic=%v", b, ok)
				}
				if u := r.URL.String(); strings.Contains(u, "sek") || strings.Contains(u, "gho_t0ken") {
					t.Errorf("secret or token in URL: %s", u)
				}
				buf := make([]byte, 100)
				n, _ := r.Body.Read(buf)
				if string(buf[:n]) != `{"access_token":"gho_t0ken"}` {
					t.Errorf("body %q", buf[:n])
				}
				w.WriteHeader(tt.status)
			})
			err := c.Revoke(context.Background(), "gho_t0ken")
			if (err == nil) != tt.ok || (err != nil && !errors.Is(err, ErrUnavailable)) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestLoadConfig(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	base := map[string]string{"GITHUB_CLIENT_ID": "i", "GITHUB_CLIENT_SECRET": "s"}
	c, err := LoadConfig(env(base), false)
	if err != nil || c.AuthorizeURL != "https://github.com" || c.OAuthURL != "https://github.com" || c.APIURL != "https://api.github.com" {
		t.Fatalf("defaults: %+v %v", c, err)
	}
	local := map[string]string{"GITHUB_CLIENT_ID": "i", "GITHUB_CLIENT_SECRET": "s",
		"GITHUB_AUTHORIZE_URL": "http://localhost:8081/", "GITHUB_OAUTH_URL": "http://fake-github:8081", "GITHUB_API_URL": "http://fake-github:8081"}
	if c, err = LoadConfig(env(local), true); err != nil || c.AuthorizeURL != "http://localhost:8081" {
		t.Fatalf("local: %+v %v", c, err)
	}
	if _, err = LoadConfig(env(local), false); err == nil {
		t.Error("http accepted outside local")
	}
	for _, bad := range []map[string]string{
		{"GITHUB_CLIENT_ID": "i"}, {"GITHUB_CLIENT_SECRET": "s"},
		{"GITHUB_CLIENT_ID": "i", "GITHUB_CLIENT_SECRET": "s", "GITHUB_API_URL": "https://api.github.com/v3"},
		{"GITHUB_CLIENT_ID": "i", "GITHUB_CLIENT_SECRET": "s", "GITHUB_API_URL": "ftp://x"},
	} {
		if _, err := LoadConfig(env(bad), true); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
	for _, o := range []string{"https://user:pass@h", "https://u@h/", "https://h?", "https://h#", "https://h:", "https://h:99999",
		"https://h:0", "https://h:x", "https://", "https://h/?a=b", "https://h?a=b", "https://h/#f"} {
		m := map[string]string{"GITHUB_CLIENT_ID": "i", "GITHUB_CLIENT_SECRET": "s", "GITHUB_OAUTH_URL": o}
		if _, err := LoadConfig(env(m), true); err == nil {
			t.Errorf("accepted origin %q", o)
		}
	}
	for _, o := range []string{"https://h", "https://h:8443", "http://localhost:8081", "http://[::1]:8081"} {
		m := map[string]string{"GITHUB_CLIENT_ID": "i", "GITHUB_CLIENT_SECRET": "s", "GITHUB_OAUTH_URL": o}
		if _, err := LoadConfig(env(m), true); err != nil {
			t.Errorf("rejected origin %q: %v", o, err)
		}
	}
}

func TestNeverPrintsSecret(t *testing.T) {
	c := Config{ClientID: "i", ClientSecret: "topsecret"}
	for name, v := range map[string]any{"config": c, "config ptr": &c, "client": New(c), "secret": c.ClientSecret} {
		var b strings.Builder
		slog.New(slog.NewTextHandler(&b, nil)).Info("x", "v", v)
		slog.New(slog.NewJSONHandler(&b, nil)).Info("x", "v", v)
		j, _ := json.Marshal(v)
		for _, s := range []string{fmt.Sprintf("%v", v), fmt.Sprintf("%+v", v), fmt.Sprintf("%#v", v), fmt.Sprintf("%s", v), b.String(), string(j)} {
			if strings.Contains(s, "topsecret") {
				t.Errorf("%s leaked the secret: %s", name, s)
			}
		}
	}
}

func TestCheckToken(t *testing.T) {
	const ok = `{"app":{"client_id":"cid"},"user":{"id":1001,"login":"ada"},"scopes":["read:user","user:email"],"token":"echo"}`
	tests := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"ok", 200, ok, nil},
		{"another app's token", 200, `{"app":{"client_id":"other"},"user":{"id":1001,"login":"ada"}}`, ErrRejected},
		{"no app in the answer", 200, `{"user":{"id":1001,"login":"ada"}}`, ErrRejected},
		{"unknown or revoked token", 404, `{"message":"Not Found"}`, ErrRejected},
		{"our credentials refused", 401, `{"message":"Bad credentials"}`, ErrUnavailable},
		{"422", 422, `{}`, ErrUnavailable},
		{"500", 500, `oops`, ErrUnavailable},
		{"no user", 200, `{"app":{"client_id":"cid"}}`, ErrUnavailable},
		{"bad json", 200, `<html>`, ErrUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				id, sec, _ := r.BasicAuth()
				var in map[string]string
				_ = json.NewDecoder(r.Body).Decode(&in)
				if r.Method != http.MethodPost || r.URL.Path != "/applications/cid/token" || id != "cid" || sec != "sek" ||
					in["access_token"] != "gho_tok" || r.URL.RawQuery != "" {
					t.Errorf("request: %s %s basic=%q/%q body=%v", r.Method, r.URL, id, sec, in)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})
			info, err := c.CheckToken(context.Background(), "gho_tok")
			if (tt.want == nil) != (err == nil) || (tt.want != nil && !errors.Is(err, tt.want)) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if tt.want == nil && (info.UserID != 1001 || info.Login != "ada" || !HasScope(info.Scopes, "user:email")) {
				t.Fatalf("info = %+v", info)
			}
			if err != nil && strings.Contains(err.Error(), "gho_tok") {
				t.Fatalf("error leaks the token: %v", err)
			}
		})
	}
}
