package github

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestUserByLogin(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   error
		user   User
	}{
		{"ok", 200, `{"id":1001,"login":"Ada","type":"User"}`, nil, User{ID: 1001, Login: "Ada"}},
		{"type absent", 200, `{"id":5,"login":"x"}`, nil, User{ID: 5, Login: "x"}},
		{"unknown login", 404, `{"message":"Not Found"}`, ErrNoSuchUser, User{}},
		{"organization", 200, `{"id":9,"login":"acme","type":"Organization"}`, ErrNoSuchUser, User{}},
		{"rate limited", 403, `{}`, ErrUnavailable, User{}},
		{"server error", 502, ``, ErrUnavailable, User{}},
		{"no id", 200, `{"login":"x"}`, ErrUnavailable, User{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/users/ada" || r.Header.Get("Authorization") != "" || r.Header.Get("User-Agent") == "" {
					t.Errorf("bad request: %s %v", r.URL.Path, r.Header)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})
			u, err := c.UserByLogin(context.Background(), "ada")
			if tt.want != nil {
				if !errors.Is(err, tt.want) {
					t.Fatalf("err = %v, want %v", err, tt.want)
				}
				return
			}
			if err != nil || u.ID != tt.user.ID || u.Login != tt.user.Login {
				t.Fatalf("%+v %v", u, err)
			}
		})
	}
}

func TestLoadAPIConfig(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	c, err := LoadAPIConfig(env(nil), false)
	if err != nil || c.APIURL != "https://api.github.com" || c.ClientID != "" {
		t.Fatalf("defaults: %+v %v", c, err)
	}
	if c, err = LoadAPIConfig(env(map[string]string{"GITHUB_API_URL": "http://fake-github:8081/"}), true); err != nil || c.APIURL != "http://fake-github:8081" {
		t.Fatalf("local: %+v %v", c, err)
	}
	for _, bad := range []string{"http://fake-github:8081", "https://api.github.com/v3", "ftp://x"} {
		if _, err := LoadAPIConfig(env(map[string]string{"GITHUB_API_URL": bad}), false); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
