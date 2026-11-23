package fakegithub_test

import "testing"

func TestPublicUserLookup(t *testing.T) {
	e := newEnv(t)
	resp, u := e.get("/users/ada", "")
	if resp.StatusCode != 200 || u["id"] != float64(1001) || u["login"] != "ada" || u["type"] != "User" {
		t.Fatalf("ada: %d %v", resp.StatusCode, u)
	}
	if _, has := u["email"]; has {
		t.Errorf("the public lookup must not carry an email: %v", u)
	}
	if resp, u = e.get("/users/CY", ""); resp.StatusCode != 200 || u["login"] != "cy" {
		t.Errorf("lookup is case-insensitive like GitHub: %d %v", resp.StatusCode, u)
	}
	if resp, _ = e.get("/users/nobody", ""); resp.StatusCode != 404 {
		t.Errorf("unknown login: %d", resp.StatusCode)
	}
}
