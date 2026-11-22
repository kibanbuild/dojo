//go:build integration

package server_test

import (
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kibanbuild/dojo/internal/server"
)

func getMe(t *testing.T, token string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, edgeURL(t)+"/v1/me", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: server.SessionCookie, Value: token})
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(b)
}

func TestMeThroughTheEdge(t *testing.T) {
	resetLearners(t, "777001")
	token := strings.Repeat("Z", 43)
	hash := `\x` + hex.EncodeToString(server.HashToken(token))
	asOwner(t, `INSERT INTO learners (org_id, github_id, github_login, username, language, age_confirmed_at, terms_accepted_at, usage_stats)
		SELECT id, 777001, 'Grace', 'grace', 'en', now(), now(), true FROM orgs WHERE slug = 'public'`)
	asOwner(t, `INSERT INTO sessions (org_id, learner_id, token_hash)
		SELECT org_id, id, '`+hash+`' FROM learners WHERE github_id = 777001`)

	code, body := getMe(t, token)
	var me map[string]any
	if code != http.StatusOK || json.Unmarshal([]byte(body), &me) != nil || me["username"] != "grace" || me["github_login"] != "Grace" {
		t.Fatalf("active: got %d %q", code, body)
	}
	if _, leaked := me["github_id"]; leaked {
		t.Fatalf("github_id leaked: %q", body)
	}

	asOwner(t, `UPDATE learners SET suspended_at = now() WHERE github_id = 777001`)
	if code, body := getMe(t, token); code != http.StatusForbidden || body != "{\"error\":\"account_suspended\"}\n" {
		t.Fatalf("suspended: got %d %q", code, body)
	}

	asOwner(t, `UPDATE learners SET suspended_at = NULL WHERE github_id = 777001`)
	asOwner(t, `UPDATE sessions SET revoked_at = now() WHERE token_hash = '`+hash+`'`)
	if code, body := getMe(t, token); code != http.StatusUnauthorized || body != "{\"error\":\"unauthenticated\"}\n" {
		t.Fatalf("revoked: got %d %q", code, body)
	}
}
