package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kibanyadev/dojo/internal/state"
)

const (
	ghSecret  = "gho_SECRETGITHUBTOKEN"
	dojoToken = "dojo_abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ0"
)

// stack is one httptest server playing GitHub and the Dojo API at once.
type stack struct {
	srv *httptest.Server
	mu  sync.Mutex
	// polls answers successive device polls; the last repeats.
	polls    []map[string]any
	pollN    int
	expires  int
	interval int
	// API behaviour.
	cliStatus  int
	cliBody    string
	retryAfter string
	meStatus   int
	meBody     string
	delStatus  int
	cliReq     map[string]string
	cliCalls   int
	delBearer  string
	delCalls   int
	meBearers  []string
	codeCalls  int
	scope      string
	clientID   string
	dropConns  bool
}

func newStack(t *testing.T) *stack {
	t.Helper()
	s := &stack{expires: 900, interval: 5, cliStatus: 201, cliBody: `{"token":"` + dojoToken + `"}`,
		meStatus: 200, meBody: `{"username":"ada","github_login":"ada"}`, delStatus: 204}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login/device/code", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		s.mu.Lock()
		s.codeCalls++
		s.scope, s.clientID = r.PostForm.Get("scope"), r.PostForm.Get("client_id")
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"device_code": "dc1", "user_code": "ABCD-EFGH",
			"verification_uri": "https://example.test/login/device", "expires_in": s.expires, "interval": s.interval})
	})
	mux.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		s.mu.Lock()
		i := min(s.pollN, len(s.polls)-1)
		s.pollN++
		body := s.polls[i]
		drop := s.dropConns
		s.mu.Unlock()
		if drop {
			hj, _ := w.(http.Hijacker)
			c, _, _ := hj.Hijack()
			_ = c.Close()
			return
		}
		if r.PostForm.Get("device_code") != "dc1" || r.PostForm.Get("grant_type") != deviceGrant {
			body = map[string]any{"error": "incorrect_device_code"}
		}
		_ = json.NewEncoder(w).Encode(body)
	})
	mux.HandleFunc("POST /v1/auth/cli", func(w http.ResponseWriter, r *http.Request) {
		var m map[string]string
		_ = json.NewDecoder(r.Body).Decode(&m)
		s.mu.Lock()
		s.cliReq, s.cliCalls = m, s.cliCalls+1
		s.mu.Unlock()
		if s.retryAfter != "" {
			w.Header().Set("Retry-After", s.retryAfter)
		}
		w.WriteHeader(s.cliStatus)
		_, _ = w.Write([]byte(s.cliBody))
	})
	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.meBearers = append(s.meBearers, r.Header.Get("Authorization"))
		s.mu.Unlock()
		w.WriteHeader(s.meStatus)
		_, _ = w.Write([]byte(s.meBody))
	})
	mux.HandleFunc("DELETE /v1/auth/cli", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.delBearer, s.delCalls = r.Header.Get("Authorization"), s.delCalls+1
		s.mu.Unlock()
		w.WriteHeader(s.delStatus)
		if s.delStatus == 401 {
			_, _ = w.Write([]byte(`{"error":"unauthenticated"}`))
		}
	})
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

// seen is a locked copy of what the handlers recorded.
type seen struct {
	cliReq                               map[string]string
	cliCalls, delCalls, codeCalls, pollN int
	delBearer, scope, clientID           string
	meBearers                            []string
}

func (s *stack) seen() seen {
	s.mu.Lock()
	defer s.mu.Unlock()
	return seen{s.cliReq, s.cliCalls, s.delCalls, s.codeCalls, s.pollN, s.delBearer, s.scope, s.clientID, append([]string(nil), s.meBearers...)}
}

func pending() map[string]any { return map[string]any{"error": "authorization_pending"} }
func granted() map[string]any {
	return map[string]any{"access_token": ghSecret, "token_type": "bearer"}
}

// loginEnv wires an env's app to a stack with a fake clock that Sleep advances.
func loginEnv(t *testing.T, s *stack) (*env, *[]time.Duration) {
	t.Helper()
	e := newEnv(t)
	clock := time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)
	slept := &[]time.Duration{}
	e.app.Now = func() time.Time { return clock }
	e.app.Sleep = func(ctx context.Context, d time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		*slept = append(*slept, d)
		clock = clock.Add(d)
		return nil
	}
	e.app.Login = LoginConfig{APIURL: s.srv.URL, GitHubURL: s.srv.URL, GitHubOAuth: "dojo-web-local"}
	return e, slept
}

func tokenOnDisk(t *testing.T) string {
	t.Helper()
	tok, err := state.LoadToken()
	if errors.Is(err, state.ErrNoToken) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestLoginHappyPath(t *testing.T) {
	s := newStack(t)
	s.polls = []map[string]any{pending(), pending(), granted()}
	e, slept := loginEnv(t, s)

	if err := e.run(t, "login"); err != nil {
		t.Fatalf("login: %v\n%s", err, e.out)
	}
	out := e.out.String()
	for _, want := range []string{"https://example.test/login/device", "ABCD-EFGH", "Logged in as ada"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if len(*slept) != 3 || (*slept)[0] != 5*time.Second {
		t.Errorf("polls slept %v, want three waits of the 5s interval", *slept)
	}
	if s.seen().scope != "read:user user:email" || s.seen().clientID != "dojo-web-local" {
		t.Errorf("device request scope %q client %q", s.seen().scope, s.seen().clientID)
	}
	if tokenOnDisk(t) != dojoToken {
		t.Errorf("stored token %q", tokenOnDisk(t))
	}
	r := s.seen().cliReq
	if r["access_token"] != ghSecret || r["device_name"] == "" || !strings.Contains(r["os"], "/") || r["cli_version"] != "dev" {
		t.Errorf("api request %v", r)
	}
	if got := s.seen().meBearers; len(got) == 0 || got[len(got)-1] != "Bearer "+dojoToken {
		t.Errorf("me bearers %v", got)
	}
}

func TestLoginNeverLeaksTokens(t *testing.T) {
	s := newStack(t)
	s.polls = []map[string]any{granted()}
	e, _ := loginEnv(t, s)
	if err := e.run(t, "login"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(e.out.String(), ghSecret) || strings.Contains(e.out.String(), dojoToken) {
		t.Errorf("a token reached stdout:\n%s", e.out)
	}
	// The GitHub token is on no file in DOJO_HOME; the dojo token only in the token file.
	home, _ := state.Dir()
	entries, _ := os.ReadDir(home)
	for _, en := range entries {
		raw, _ := os.ReadFile(home + "/" + en.Name())
		if strings.Contains(string(raw), ghSecret) {
			t.Errorf("GitHub token written to %s", en.Name())
		}
		if en.Name() != "token" && strings.Contains(string(raw), dojoToken) {
			t.Errorf("dojo token written to %s", en.Name())
		}
	}
	// Errors do not carry them either.
	s2 := newStack(t)
	s2.polls = []map[string]any{granted()}
	s2.cliStatus, s2.cliBody = 401, `{"error":"github_rejected"}`
	e2, _ := loginEnv(t, s2)
	err := e2.run(t, "login")
	if err == nil || strings.Contains(err.Error(), ghSecret) {
		t.Errorf("error %v", err)
	}
}

func TestLoginTokenFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	s := newStack(t)
	s.polls = []map[string]any{granted()}
	e, _ := loginEnv(t, s)
	if err := e.run(t, "login"); err != nil {
		t.Fatal(err)
	}
	p, _ := state.TokenPath()
	if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("token file: %v %v", fi, err)
	}
	if fi, _ := os.Stat(strings.TrimSuffix(p, "/token")); fi.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v", fi.Mode().Perm())
	}
}

func TestLoginSlowDownRaisesTheInterval(t *testing.T) {
	s := newStack(t)
	s.interval = 5
	s.polls = []map[string]any{pending(), {"error": "slow_down", "interval": 10}, pending(), granted()}
	e, slept := loginEnv(t, s)
	if err := e.run(t, "login"); err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{5 * time.Second, 5 * time.Second, 10 * time.Second, 10 * time.Second}
	if len(*slept) != len(want) {
		t.Fatalf("waits %v, want %v", *slept, want)
	}
	for i := range want {
		if (*slept)[i] != want[i] {
			t.Errorf("waits %v, want %v", *slept, want)
			break
		}
	}
}

func TestLoginSlowDownWithoutIntervalAddsFive(t *testing.T) {
	s := newStack(t)
	s.polls = []map[string]any{{"error": "slow_down"}, granted()}
	e, slept := loginEnv(t, s)
	if err := e.run(t, "login"); err != nil {
		t.Fatal(err)
	}
	if len(*slept) != 2 || (*slept)[1] != 10*time.Second {
		t.Errorf("waits %v", *slept)
	}
}

func TestLoginDeviceFlowEnds(t *testing.T) {
	for name, tc := range map[string]struct {
		poll map[string]any
		want string
	}{
		"expired_token": {map[string]any{"error": "expired_token"}, "code expired"},
		"token_expired": {map[string]any{"error": "token_expired"}, "code expired"},
		"denied":        {map[string]any{"error": "access_denied"}, "cancelled on GitHub"},
		"other":         {map[string]any{"error": "device_flow_disabled"}, "device_flow_disabled"},
	} {
		t.Run(name, func(t *testing.T) {
			s := newStack(t)
			s.polls = []map[string]any{pending(), tc.poll}
			e, _ := loginEnv(t, s)
			err := e.run(t, "login")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if s.seen().cliCalls != 0 || tokenOnDisk(t) != "" {
				t.Error("a failed flow reached the API or wrote a token")
			}
		})
	}
}

func TestLoginStopsAtTheDeadline(t *testing.T) {
	s := newStack(t)
	s.expires, s.interval = 20, 5
	s.polls = []map[string]any{pending()}
	e, slept := loginEnv(t, s)
	err := e.run(t, "login")
	if err == nil || !strings.Contains(err.Error(), "code expired") {
		t.Fatalf("err = %v", err)
	}
	if len(*slept) > 5 {
		t.Errorf("kept polling past the deadline: %v", *slept)
	}
}

func TestLoginCancelled(t *testing.T) {
	s := newStack(t)
	s.polls = []map[string]any{pending()}
	e, _ := loginEnv(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	e.app.Sleep = func(context.Context, time.Duration) error { cancel(); return ctx.Err() }
	err := e.app.Run(ctx, []string{"login"})
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Errorf("err = %v", err)
	}
}

func TestLoginSurvivesAFewNetworkBlips(t *testing.T) {
	s := newStack(t)
	s.polls = []map[string]any{granted()}
	s.dropConns = true
	e, _ := loginEnv(t, s)
	err := e.run(t, "login")
	if err == nil || !strings.Contains(err.Error(), "lost contact with GitHub") {
		t.Errorf("err = %v", err)
	}
	if s.seen().pollN != maxPollErrors {
		t.Errorf("polls = %d, want %d", s.seen().pollN, maxPollErrors)
	}
}

func TestLoginAPIErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
		retry  string
		want   string
	}{
		"signup":      {403, `{"error":"signup_required"}`, "", "sign up at "},
		"suspended":   {403, `{"error":"account_suspended"}`, "", "suspended"},
		"rejected":    {401, `{"error":"github_rejected"}`, "", "run dojo login again"},
		"rate":        {429, `{"error":"rate_limited"}`, "42", "wait 42 seconds"},
		"rate bare":   {429, `{"error":"rate_limited"}`, "", "wait a minute"},
		"github down": {502, `{"error":"github_unavailable"}`, "", "could not reach GitHub"},
		"bad":         {400, `{"error":"bad_request"}`, "", "up to date"},
		"server":      {500, `{"error":"server_error"}`, "", "(500)"},
	} {
		t.Run(name, func(t *testing.T) {
			s := newStack(t)
			s.polls = []map[string]any{granted()}
			s.cliStatus, s.cliBody, s.retryAfter = tc.status, tc.body, tc.retry
			e, _ := loginEnv(t, s)
			err := e.run(t, "login")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if tokenOnDisk(t) != "" {
				t.Error("a refused login stored a token")
			}
			if name == "signup" && !strings.Contains(err.Error(), s.srv.URL+"/app") {
				t.Errorf("signup message lacks the app URL: %v", err)
			}
		})
	}
}

func TestLoginAlreadyLoggedIn(t *testing.T) {
	s := newStack(t)
	s.polls = []map[string]any{granted()}
	e, _ := loginEnv(t, s)
	if err := state.SaveToken(dojoToken); err != nil {
		t.Fatal(err)
	}
	if err := e.run(t, "login"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.out.String(), "Already logged in as ada") || s.seen().codeCalls != 0 || s.seen().cliCalls != 0 {
		t.Errorf("out %q codeCalls %d cliCalls %d", e.out, s.seen().codeCalls, s.seen().cliCalls)
	}
	// --force starts a new flow anyway.
	if err := e.run(t, "login", "--force"); err != nil || s.seen().codeCalls != 1 || s.seen().cliCalls != 1 {
		t.Errorf("force: %v codeCalls %d cliCalls %d", err, s.seen().codeCalls, s.seen().cliCalls)
	}
}

func TestLoginWithAStaleTokenStartsAFlow(t *testing.T) {
	s := newStack(t)
	s.polls = []map[string]any{granted()}
	s.meStatus, s.meBody = 401, `{"error":"unauthenticated"}`
	e, _ := loginEnv(t, s)
	if err := state.SaveToken("dojo_stale"); err != nil {
		t.Fatal(err)
	}
	// The new token is checked by the same /v1/me, which the stack still refuses: login then says plain "Logged in."
	if err := e.run(t, "login"); err != nil {
		t.Fatal(err)
	}
	if s.seen().codeCalls != 1 || tokenOnDisk(t) != dojoToken {
		t.Errorf("codeCalls %d token %q", s.seen().codeCalls, tokenOnDisk(t))
	}
	if !strings.Contains(e.out.String(), "Logged in.") {
		t.Errorf("out %q", e.out)
	}
}

func TestLoginWithASuspendedSavedAccount(t *testing.T) {
	s := newStack(t)
	s.meStatus, s.meBody = 403, `{"error":"account_suspended"}`
	e, _ := loginEnv(t, s)
	_ = state.SaveToken(dojoToken)
	if err := e.run(t, "login"); err == nil || !strings.Contains(err.Error(), "suspended") || s.seen().codeCalls != 0 {
		t.Errorf("err %v codeCalls %d", err, s.seen().codeCalls)
	}
}

func TestLoginConfigErrors(t *testing.T) {
	e := newEnv(t)
	e.app.Login = LoginConfig{}
	if err := e.run(t, "login"); err == nil || !strings.Contains(err.Error(), "DOJO_API_URL") {
		t.Errorf("no api url: %v", err)
	}
	e.app.Login = LoginConfig{APIURL: "http://127.0.0.1:1"}
	if err := e.run(t, "login"); err == nil || !strings.Contains(err.Error(), "DOJO_GITHUB_CLIENT_ID") {
		t.Errorf("no client id: %v", err)
	}
	if err := e.run(t, "login", "--api-url", "ftp://x", "--client-id", "c"); err == nil || !strings.Contains(err.Error(), "not an http(s) URL") {
		t.Errorf("bad url: %v", err)
	}
	if err := e.run(t, "login", "--api-url", "http://dojo.example.com", "--client-id", "c"); err == nil || !strings.Contains(err.Error(), "must use https") {
		t.Errorf("plain http to a remote host: %v", err)
	}
}

func TestPrintableDropsTerminalEscapes(t *testing.T) {
	if got := printable("ABCD-\x1b[2JEFGH\u202e"); got != "ABCD-[2JEFGH" {
		t.Fatalf("got %q", got)
	}
}

func TestLoginFlagsOverrideConfig(t *testing.T) {
	s := newStack(t)
	s.polls = []map[string]any{granted()}
	e, _ := loginEnv(t, s)
	e.app.Login = LoginConfig{APIURL: "http://127.0.0.1:1", GitHubURL: "http://127.0.0.1:1", GitHubOAuth: "wrong"}
	if err := e.run(t, "login", "--api-url", s.srv.URL+"/", "--github-url", s.srv.URL, "--client-id", "flagged"); err != nil {
		t.Fatal(err)
	}
	if s.seen().clientID != "flagged" {
		t.Errorf("client id %q", s.seen().clientID)
	}
}

func TestSanitizers(t *testing.T) {
	long := strings.Repeat("é", 80)
	for in, want := range map[string]string{
		"":                "unknown",
		"  ":              "unknown",
		"ana's mac\x00\n": "ana's mac",
		" pad ":           "pad",
		"a\u200bb":        "ab",
		"bad\xffutf":      "badutf",
		long:              strings.Repeat("é", 64),
	} {
		if got := sanitizeDeviceName(in); got != want {
			t.Errorf("sanitizeDeviceName(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"v1.2.3-rc1+abc": "v1.2.3-rc1+abc", "dev (x)": "devx", "": "dev", strings.Repeat("a", 40): strings.Repeat("a", 32)} {
		if got := cleanVersion(in); got != want {
			t.Errorf("cleanVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLogout(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		gone   bool
		err    string
	}{
		"204": {204, true, ""},
		"401": {401, true, ""},
		"500": {500, false, "still logged in"},
		"429": {429, false, "still logged in"},
	} {
		t.Run(name, func(t *testing.T) {
			s := newStack(t)
			s.delStatus = tc.status
			e, _ := loginEnv(t, s)
			_ = state.SaveToken(dojoToken)
			err := e.run(t, "logout")
			if tc.err == "" && err != nil || tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)) {
				t.Fatalf("err = %v", err)
			}
			if s.seen().delBearer != "Bearer "+dojoToken {
				t.Errorf("bearer %q", s.seen().delBearer)
			}
			if gone := tokenOnDisk(t) == ""; gone != tc.gone {
				t.Errorf("token gone = %v, want %v", gone, tc.gone)
			}
		})
	}
}

func TestLogoutNetworkErrorKeepsTheFile(t *testing.T) {
	s := newStack(t)
	e, _ := loginEnv(t, s)
	e.app.Login.APIURL = s.srv.URL
	s.srv.Close()
	_ = state.SaveToken(dojoToken)
	err := e.run(t, "logout")
	if err == nil || !strings.Contains(err.Error(), "--local") {
		t.Fatalf("err = %v", err)
	}
	if tokenOnDisk(t) != dojoToken {
		t.Error("the token was removed although the API was not told")
	}
	if strings.Contains(err.Error(), dojoToken) {
		t.Error("token in the error")
	}
}

func TestLogoutLocalSkipsTheAPI(t *testing.T) {
	s := newStack(t)
	e, _ := loginEnv(t, s)
	_ = state.SaveToken(dojoToken)
	if err := e.run(t, "logout", "--local"); err != nil {
		t.Fatal(err)
	}
	if s.seen().delCalls != 0 || tokenOnDisk(t) != "" {
		t.Errorf("delCalls %d token %q", s.seen().delCalls, tokenOnDisk(t))
	}
}

func TestLogoutNotLoggedIn(t *testing.T) {
	s := newStack(t)
	e, _ := loginEnv(t, s)
	if err := e.run(t, "logout"); err != nil || !strings.Contains(e.out.String(), "Not logged in") || s.seen().delCalls != 0 {
		t.Errorf("err %v out %q", err, e.out)
	}
}

func TestLoginRejectsArguments(t *testing.T) {
	e := newEnv(t)
	if err := e.run(t, "login", "extra"); err == nil {
		t.Error("login took an argument")
	}
}
