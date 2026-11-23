package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestBootstrapRefusesBeforeAnyWork(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	tests := []struct {
		name string
		args []string
		env  map[string]string
		want string
	}{
		{"no login", nil, map[string]string{"BOOTSTRAP_DATABASE_URL": "postgres://x"}, "usage"},
		{"two logins", []string{"a", "b"}, map[string]string{"BOOTSTRAP_DATABASE_URL": "postgres://x"}, "usage"},
		{"no migration DSN", []string{"ada"}, map[string]string{"DATABASE_URL": "postgres://dojo_admin@x"}, "BOOTSTRAP_DATABASE_URL is required"},
		{"bad login", []string{"-ada"}, map[string]string{"BOOTSTRAP_DATABASE_URL": "postgres://x"}, "not a valid GitHub login"},
		{"bad api origin", []string{"ada"}, map[string]string{"BOOTSTRAP_DATABASE_URL": "postgres://x", "GITHUB_API_URL": "http://evil"}, "GITHUB_API_URL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			err := bootstrap(context.Background(), tt.args, env(tt.env), &out)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
			if out.Len() != 0 {
				t.Fatalf("printed %q", out.String())
			}
		})
	}
}
