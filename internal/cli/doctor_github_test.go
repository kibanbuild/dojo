package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/kibanbuild/dojo/internal/doctor"
	"github.com/kibanbuild/dojo/internal/state"
)

const cliKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA ada@laptop"

type ghAPI struct {
	srv    *httptest.Server
	mu     sync.Mutex
	events []string
	me     int
}

// newGHAPI serves /v1/me (status me) and records /v1/events bodies.
func newGHAPI(t *testing.T, me int) *ghAPI {
	t.Helper()
	a := &ghAPI{me: me}
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/me":
			w.WriteHeader(a.me)
			if a.me == 200 {
				_, _ = w.Write([]byte(`{"id":"x","username":"ada","github_login":"ada","github_id":1001}`))
			}
		case "/v1/events":
			b, _ := io.ReadAll(r.Body)
			a.mu.Lock()
			a.events = append(a.events, string(b))
			a.mu.Unlock()
			_, _ = w.Write([]byte(`{"accepted":1,"duplicates":0,"ignored":0}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(a.srv.Close)
	return a
}

func githubEnv(t *testing.T, me int, gh http.HandlerFunc) (*env, *ghAPI, *atomic.Int32) {
	t.Helper()
	api := newGHAPI(t, me)
	var calls atomic.Int32
	ghSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		gh(w, r)
	}))
	t.Cleanup(ghSrv.Close)
	e := newEnv(t)
	e.app.Login = LoginConfig{APIURL: api.srv.URL}
	e.app.Getenv = func(k string) string {
		if k == "DOJO_CLI_GITHUB_API_URL" {
			return ghSrv.URL
		}
		return ""
	}
	e.app.Probe = func(context.Context) doctor.Machine {
		m := goodMachine
		m.SigningPub, m.GitEmail = cliKey, "1001+ada@users.noreply.github.com"
		return m
	}
	e.app.ProbeGitHub = func(_ context.Context, m *doctor.Machine) { m.CloneOverSSH, m.SSHOfferedKeys = true, []string{cliKey} }
	return e, api, &calls
}

func allGood(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/users/ada/ssh_signing_keys", "/users/ada/keys":
		_, _ = w.Write([]byte(`[{"id":1,"key":"` + cliKey + `"}]`))
	case "/repos/ada/dojo":
		_, _ = w.Write([]byte(`{"name":"dojo","private":false}`))
	case "/repos/ada/dojo/rules/branches/main":
		_, _ = w.Write([]byte(`[{"type":"required_signatures"},{"type":"non_fast_forward"},{"type":"deletion"}]`))
	case "/repos/ada/dojo/contents/AGENTS.md":
		_, _ = w.Write([]byte(`{"type":"file"}`))
	default:
		w.WriteHeader(404)
	}
}

func TestDoctorGitHubChecksSendTheirIds(t *testing.T) {
	e, api, calls := githubEnv(t, 200, allGood)
	if err := state.SaveToken("tok"); err != nil {
		t.Fatal(err)
	}
	if err := e.run(t, "doctor"); err != nil {
		t.Fatalf("doctor: %v\n%s", err, e.out)
	}
	if n := calls.Load(); n != 5 {
		t.Errorf("GitHub calls = %d, want 5 (budget 7)", n)
	}
	if len(api.events) != 1 {
		t.Fatalf("events = %v", api.events)
	}
	for _, id := range []string{"key_signing", "key_auth", "repo", "ruleset", "agents_md", "git_email_github"} {
		if !strings.Contains(api.events[0], `"id":"`+id+`","status":"ok"`) {
			t.Errorf("doctor.run lacks %s ok: %s", id, api.events[0])
		}
	}
	if strings.Contains(e.out.String(), "log in to check") {
		t.Errorf("output:\n%s", e.out)
	}
}

func TestDoctorLoggedOutSkipsGitHubAndSendsNothingForIt(t *testing.T) {
	e, _, calls := githubEnv(t, 200, allGood)
	if err := e.run(t, "doctor"); err != nil {
		t.Fatalf("doctor: %v\n%s", err, e.out)
	}
	if calls.Load() != 0 {
		t.Errorf("GitHub calls = %d", calls.Load())
	}
	if !strings.Contains(e.out.String(), "log in to check your GitHub steps") {
		t.Errorf("output:\n%s", e.out)
	}
	q, _ := state.LoadEvents()
	if len(q.Events) != 1 {
		t.Fatalf("queue = %+v", q.Events)
	}
	for _, c := range q.Events[0].Checks {
		switch c.ID {
		case "key_signing", "key_auth", "repo", "ruleset", "agents_md", "git_email_github":
			t.Errorf("queued %s while logged out", c.ID)
		}
	}
}

func TestDoctorRejectedTokenSkipsGitHub(t *testing.T) {
	e, _, calls := githubEnv(t, 401, allGood)
	_ = state.SaveToken("tok")
	if err := e.run(t, "doctor"); err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if calls.Load() != 0 || !strings.Contains(e.out.String(), "log in to check your GitHub steps") {
		t.Errorf("calls=%d output:\n%s", calls.Load(), e.out)
	}
}

func TestDoctorRateLimitedShowsUnknownAndStillExitsReady(t *testing.T) {
	e, api, calls := githubEnv(t, 200, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", "9999999999")
		w.WriteHeader(http.StatusForbidden)
	})
	_ = state.SaveToken("tok")
	if err := e.run(t, "doctor"); err != nil {
		t.Fatalf("a rate limit must not make the machine not ready: %v\n%s", err, e.out)
	}
	if calls.Load() != 1 || !strings.Contains(e.out.String(), "? GitHub is limiting requests, try again") {
		t.Errorf("calls=%d output:\n%s", calls.Load(), e.out)
	}
	if len(api.events) != 1 || !strings.Contains(api.events[0], `"id":"repo","status":"unknown"`) {
		t.Errorf("events = %v", api.events)
	}
}

func TestDoctorBadGitHubURLSkipsWithALine(t *testing.T) {
	e, _, _ := githubEnv(t, 200, allGood)
	e.app.Getenv = func(k string) string {
		if k == "DOJO_CLI_GITHUB_API_URL" {
			return "http://example.com/x"
		}
		return ""
	}
	_ = state.SaveToken("tok")
	if err := e.run(t, "doctor"); err != nil || !strings.Contains(e.out.String(), "GitHub steps not checked") {
		t.Errorf("err=%v output:\n%s", err, e.out)
	}
}
