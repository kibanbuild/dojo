//go:build integration

package ghread_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"testing"

	"github.com/kibanyadev/dojo/internal/ghread"
)

func fake(t *testing.T) string {
	base := os.Getenv("DOJO_FAKE_GITHUB_URL")
	if base == "" {
		t.Fatal("DOJO_FAKE_GITHUB_URL is not set: run `make test-integration`")
	}
	return base
}

func control(t *testing.T, method, url, body string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, bytes.NewReader([]byte(body)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("%s %s: %d", method, url, resp.StatusCode)
	}
}

func TestReadsAgainstComposeFake(t *testing.T) {
	base := fake(t)
	t.Cleanup(func() { control(t, "POST", base+"/_fake/reset", "") })
	control(t, "PUT", base+"/_fake/users/bob/keys", `[{"id":11,"key":"ssh-ed25519 KEY"}]`)
	control(t, "PUT", base+"/_fake/users/bob/repos/dojo",
		`{"rules":{"main":[{"type":"required_signatures","ruleset_id":1}]},"files":{"AGENTS.md":"# rules"}}`)
	c, err := ghread.New(ghread.Config{BaseURL: base})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if u, err := c.UserByID(ctx, 1002); err != nil || u.Login != "bob" {
		t.Fatalf("user: %+v %v", u, err)
	}
	if k, err := c.Keys(ctx, "bob"); err != nil || len(k) != 1 || k[0].ID != 11 {
		t.Fatalf("keys: %+v %v", k, err)
	}
	if r, err := c.Repo(ctx, "bob", "dojo"); err != nil || r.Private {
		t.Fatalf("repo: %+v %v", r, err)
	}
	if r, err := c.BranchRules(ctx, "bob", "dojo", "main"); err != nil || len(r) != 1 {
		t.Fatalf("rules: %+v %v", r, err)
	}
	if f, err := c.Contents(ctx, "bob", "dojo", "AGENTS.md"); err != nil || f.Type != "file" {
		t.Fatalf("contents: %+v %v", f, err)
	}
	if _, err := c.Repo(ctx, "bob", "missing"); !errors.Is(err, ghread.ErrNotFound) {
		t.Fatalf("missing repo: %v", err)
	}
}
