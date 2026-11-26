package cli

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kibanbuild/dojo/internal/state"
)

const hostedHint = "Turn off the hosted default in Settings to use a local lab."

// meAPI answers /v1/me with a hosted_default and counts the bundle and event calls it must not get.
func meAPI(t *testing.T, body string, status int) (url string, others *atomic.Int32) {
	t.Helper()
	others = &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/me" {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
			return
		}
		others.Add(1)
		w.WriteHeader(404)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, others
}

func TestStartRefusesWhileHostedLabsAreComingSoon(t *testing.T) {
	for _, c := range []struct {
		name    string
		args    []string
		me      string
		meCode  int
		offline bool
		saved   bool // hosted_default kept in the state file before the run
		want    string
		hint    bool
	}{
		{"online default on", []string{"start", "white/a"}, `{"hosted_default":true}`, 200, false, false, hostedSoon, true},
		{"online default on, flag after the ticket", []string{"start", "white/a", "--hosted"}, `{"hosted_default":true}`, 200, false, false, hostedSoon, true},
		{"online default off, --hosted", []string{"start", "--hosted", "white/a"}, `{"hosted_default":false}`, 200, false, false, hostedSoon, false},
		{"online answer beats the saved value", []string{"start", "--hosted", "white/a"}, `{"hosted_default":false}`, 200, false, true, hostedSoon, false},
		{"offline uses the saved value", []string{"start", "white/a"}, "", 0, true, true, hostedSoon, true},
		{"API error uses the saved value", []string{"start", "white/a"}, `{"error":"x"}`, 500, false, true, hostedSoon, true},
		{"offline, default off, --hosted", []string{"start", "--hosted", "white/a"}, "", 0, true, false, hostedSoon, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, api := bundleEnv(t, true)
			url, others := meAPI(t, c.me, c.meCode)
			if c.offline {
				url = "http://127.0.0.1:1"
			}
			e.app.Login = LoginConfig{APIURL: url}
			if c.saved {
				if err := (&state.State{HostedDefault: true}).Save(); err != nil {
					t.Fatal(err)
				}
			}
			err := e.run(t, c.args...)
			out := e.out.String()
			if !errors.Is(err, ErrNotReady) || !strings.HasPrefix(out, c.want) || strings.Contains(out, hostedHint) != c.hint {
				t.Fatalf("err = %v, out %q, want %q (hint %v)", err, out, c.want, c.hint)
			}
			if others.Load() != 0 || len(api.eventBodies()) != 0 || e.lab.has("recreate") {
				t.Errorf("start went on: %d other calls, events %v, lab %v", others.Load(), api.eventBodies(), e.lab.calls)
			}
			if q, _ := state.LoadEvents(); len(q.Events) != 0 {
				t.Errorf("an event was queued: %+v", q.Events)
			}
		})
	}
}

func TestStartRemembersTheHostedDefaultFromAnOnlineStart(t *testing.T) {
	e, _ := bundleEnv(t, true)
	url, _ := meAPI(t, `{"hosted_default":true}`, 200)
	e.app.Login = LoginConfig{APIURL: url}
	if err := e.run(t, "start", "white/a"); !errors.Is(err, ErrNotReady) {
		t.Fatal("want a refusal")
	}
	if st, _ := state.Load(); !st.HostedDefault {
		t.Fatal("the flag was not kept in the state file")
	}
	url, _ = meAPI(t, `{"hosted_default":false}`, 200)
	e.app.Login = LoginConfig{APIURL: url}
	if err := e.run(t, "start", "--hosted", "white/a"); !errors.Is(err, ErrNotReady) {
		t.Fatal("want a refusal")
	}
	if st, _ := state.Load(); st.HostedDefault {
		t.Fatal("the flag was not cleared")
	}
}

func TestStartWithoutHostedStillStarts(t *testing.T) {
	e := newEnv(t)
	if err := e.run(t, "start", "white/a"); err != nil {
		t.Fatal(err)
	}
	if !e.lab.has("recreate setup") {
		t.Errorf("calls = %v", e.lab.calls)
	}
}

func TestLoginKeepsTheHostedDefault(t *testing.T) {
	s := newStack(t)
	s.meBody = `{"id":"u1","username":"ada","github_login":"ada","hosted_default":true}`
	s.polls = []map[string]any{granted()}
	e, _ := loginEnv(t, s)
	if err := e.run(t, "login"); err != nil {
		t.Fatal(err)
	}
	if st, _ := state.Load(); !st.HostedDefault {
		t.Fatal("login did not keep hosted_default")
	}
	if err := e.run(t, "start", "white/a"); !errors.Is(err, ErrNotReady) || !strings.Contains(e.out.String(), hostedHint) {
		t.Fatalf("err = %v, out %q", err, e.out)
	}
}
