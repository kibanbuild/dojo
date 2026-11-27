package fakegithub_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kibanyadev/dojo/internal/fakegithub"
)

func readsServer(t *testing.T, limit int, now func() time.Time) *httptest.Server {
	t.Helper()
	users := fakegithub.DefaultUsers()
	users[0].Keys = []fakegithub.Key{{ID: 1, Key: "ssh-ed25519 AAAA"}}
	users[0].SigningKeys = []fakegithub.Key{{ID: 2, Key: "ssh-ed25519 BBBB", Title: "sign", CreatedAt: "2026-11-25T00:00:00Z"}}
	users[0].Repos = []fakegithub.Repo{
		{Name: "dojo", Rules: map[string][]fakegithub.Rule{"main": {{Type: "required_signatures", RulesetID: 7}}, "feature/x": {{Type: "deletion", RulesetID: 8}}}, Files: map[string]string{"AGENTS.md": "# hi", "a/b.md": "x"}},
		{Name: "secret", Private: true},
	}
	srv := httptest.NewServer(fakegithub.New(fakegithub.Config{Users: users, RateLimit: limit, Now: now}))
	t.Cleanup(srv.Close)
	return srv
}

func do(t *testing.T, method, url, body string) (reply, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return reply{resp.StatusCode, resp.Header}, sb.String()
}

func TestReadEndpoints(t *testing.T) {
	srv := readsServer(t, 0, nil)
	cases := []struct {
		path   string
		status int
		want   string // substring of the body
	}{
		{"/user/1001", 200, `"login":"ada"`},
		{"/user/9", 404, "Not Found"},
		{"/user/abc", 404, "Not Found"},
		{"/users/ada/keys", 200, `[{"id":1,"key":"ssh-ed25519 AAAA"}]`},
		{"/users/ADA/ssh_signing_keys", 200, `"title":"sign"`},
		{"/users/bob/keys", 200, `[]`},
		{"/users/nobody/keys", 404, "Not Found"},
		{"/repos/ada/dojo", 200, `"full_name":"ada/dojo"`},
		{"/repos/ada/secret", 404, "Not Found"},
		{"/repos/ada/missing", 404, "Not Found"},
		{"/repos/ada/dojo/rules/branches/main", 200, `[{"type":"required_signatures","ruleset_id":7}]`},
		{"/repos/ada/dojo/rules/branches/other", 200, `[]`},
		{"/repos/ada/dojo/rules/branches/feature/x", 200, `"ruleset_id":8`},
		{"/repos/ada/dojo/contents/AGENTS.md", 200, `"path":"AGENTS.md"`},
		{"/repos/ada/dojo/contents/a/b.md", 200, `"name":"b.md"`},
		{"/repos/ada/dojo/contents/nope.md", 404, "Not Found"},
		{"/repos/ada/secret/contents/x", 404, "Not Found"},
	}
	for _, c := range cases {
		resp, body := do(t, "GET", srv.URL+c.path, "")
		if resp.StatusCode != c.status || !strings.Contains(strings.ReplaceAll(body, " ", ""), strings.ReplaceAll(c.want, " ", "")) {
			t.Errorf("%s: %d %s", c.path, resp.StatusCode, body)
		}
		if resp.Header.Get("X-RateLimit-Remaining") == "" || resp.Header.Get("X-RateLimit-Reset") == "" {
			t.Errorf("%s: missing rate-limit headers", c.path)
		}
	}
}

func TestNoLimitCountsNothing(t *testing.T) {
	srv := readsServer(t, 0, nil)
	for range 3 {
		resp, _ := do(t, "GET", srv.URL+"/user/1001", "")
		if resp.Header.Get("X-RateLimit-Remaining") != "5000" {
			t.Fatalf("remaining %q", resp.Header.Get("X-RateLimit-Remaining"))
		}
	}
}

func TestRateLimit(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	srv := readsServer(t, 2, func() time.Time { return now })
	for i, wantRemaining := range []string{"1", "0"} {
		resp, _ := do(t, "GET", srv.URL+"/user/1001", "")
		if resp.StatusCode != 200 || resp.Header.Get("X-RateLimit-Remaining") != wantRemaining {
			t.Fatalf("call %d: %d remaining=%s", i, resp.StatusCode, resp.Header.Get("X-RateLimit-Remaining"))
		}
	}
	resp, _ := do(t, "GET", srv.URL+"/user/1001", "")
	if resp.StatusCode != 403 || resp.Header.Get("X-RateLimit-Remaining") != "0" || resp.Header.Get("X-RateLimit-Reset") == "" {
		t.Fatalf("limited: %d %v", resp.StatusCode, resp.Header)
	}
	now = now.Add(61 * time.Minute)
	if resp, _ = do(t, "GET", srv.URL+"/user/1001", ""); resp.StatusCode != 200 {
		t.Fatalf("after the window: %d", resp.StatusCode)
	}
}

func TestControlRoutes(t *testing.T) {
	srv := readsServer(t, 0, nil)
	if r, _ := do(t, "PUT", srv.URL+"/_fake/users/bob/keys", `[{"id":5,"key":"k"}]`); r.StatusCode != 204 {
		t.Fatalf("put keys: %d", r.StatusCode)
	}
	if _, b := do(t, "GET", srv.URL+"/users/bob/keys", ""); !strings.Contains(b, `"id":5`) {
		t.Errorf("keys: %s", b)
	}
	if r, _ := do(t, "PUT", srv.URL+"/_fake/users/bob/signing_keys", `[{"id":6,"key":"s"}]`); r.StatusCode != 204 {
		t.Fatalf("put signing: %d", r.StatusCode)
	}
	repo := `{"name":"ignored","rules":{"main":[{"type":"deletion","ruleset_id":1}]},"files":{"AGENTS.md":"x"}}`
	if r, _ := do(t, "PUT", srv.URL+"/_fake/users/bob/repos/dojo", repo); r.StatusCode != 204 {
		t.Fatalf("put repo: %d", r.StatusCode)
	}
	_, b := do(t, "GET", srv.URL+"/repos/bob/dojo", "")
	var got map[string]any
	if err := json.Unmarshal([]byte(b), &got); err != nil || got["name"] != "dojo" {
		t.Fatalf("repo name comes from the path: %s", b)
	}
	if r, _ := do(t, "DELETE", srv.URL+"/_fake/users/bob/repos/dojo", ""); r.StatusCode != 204 {
		t.Fatalf("delete: %d", r.StatusCode)
	}
	if r, _ := do(t, "GET", srv.URL+"/repos/bob/dojo", ""); r.StatusCode != 404 {
		t.Errorf("deleted repo: %d", r.StatusCode)
	}
	if r, _ := do(t, "PUT", srv.URL+"/_fake/users/nobody/keys", `[]`); r.StatusCode != 404 {
		t.Errorf("unknown login: %d", r.StatusCode)
	}
	if r, _ := do(t, "PUT", srv.URL+"/_fake/users/bob/keys", `{bad`); r.StatusCode != 400 {
		t.Errorf("bad json: %d", r.StatusCode)
	}
	if r, _ := do(t, "POST", srv.URL+"/_fake/reset", ""); r.StatusCode != 204 {
		t.Fatalf("reset: %d", r.StatusCode)
	}
	if _, b := do(t, "GET", srv.URL+"/users/bob/keys", ""); b != "[]\n" {
		t.Errorf("after reset: %q", b)
	}
}
