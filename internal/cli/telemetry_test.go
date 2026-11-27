package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kibanyadev/dojo/internal/state"
)

type teleReq struct {
	method, path, auth string
	body               string
}

// teleAPI plays the Dojo API for telemetry and the usage_stats PATCH.
type teleAPI struct {
	srv        *httptest.Server
	mu         sync.Mutex
	reqs       []teleReq
	status     int            // answer for POST /v1/telemetry
	retryAfter string         // header on a 429
	perAuth    map[string]int // status by Authorization header, wins over status
	patch      int            // answer for PATCH /v1/me
	block      chan struct{}  // when set, POST /v1/telemetry waits for it
	meBody     string
}

func newTeleAPI(t *testing.T) *teleAPI {
	t.Helper()
	a := &teleAPI{status: 204, patch: 200, meBody: `{"username":"ada","usage_stats":true}`}
	a.block = nil
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		a.mu.Lock()
		a.reqs = append(a.reqs, teleReq{r.Method, r.URL.Path, r.Header.Get("Authorization"), string(raw)})
		st, ra, blk := a.status, a.retryAfter, a.block
		if s, ok := a.perAuth[r.Header.Get("Authorization")]; ok {
			st = s
		}
		a.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/telemetry":
			if blk != nil {
				select {
				case <-blk:
				case <-r.Context().Done():
				}
			}
			if ra != "" {
				w.Header().Set("Retry-After", ra)
			}
			w.WriteHeader(st)
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/me":
			w.WriteHeader(a.patch)
			_, _ = w.Write([]byte(`{"username":"ada"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_, _ = w.Write([]byte(a.meBody))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(a.srv.Close)
	return a
}

func (a *teleAPI) posts() []teleReq {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []teleReq
	for _, r := range a.reqs {
		if r.method == http.MethodPost && r.path == "/v1/telemetry" {
			out = append(out, r)
		}
	}
	return out
}

func (a *teleAPI) patches() []teleReq {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []teleReq
	for _, r := range a.reqs {
		if r.method == http.MethodPatch {
			out = append(out, r)
		}
	}
	return out
}

type teleBody struct {
	CLIVersion string `json:"cli_version"`
	OS         string `json:"os"`
	Events     []struct {
		Command    string `json:"command"`
		Result     string `json:"result"`
		DurationMS int    `json:"duration_ms"`
	} `json:"events"`
}

func decodeBody(t *testing.T, r teleReq) teleBody {
	t.Helper()
	var b teleBody
	dec := json.NewDecoder(strings.NewReader(r.body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		t.Fatalf("body %q: %v", r.body, err)
	}
	return b
}

// teleEnv is an env wired to a teleAPI with a stderr buffer for the notice.
func teleEnv(t *testing.T) (*env, *teleAPI, *bytes.Buffer) {
	t.Helper()
	api := newTeleAPI(t)
	e := newEnv(t)
	errBuf := &bytes.Buffer{}
	e.app.Err = errBuf
	e.app.Login = LoginConfig{APIURL: api.srv.URL}
	e.app.Getenv = func(string) string { return "" }
	return e, api, errBuf
}

// shown marks the notice as already shown, as a prior command would have.
func shown(t *testing.T) {
	t.Helper()
	if err := state.SaveTelemetry(state.Telemetry{NoticeShown: true}); err != nil {
		t.Fatal(err)
	}
}

func TestNoticeOnceAndNothingSentBeforeIt(t *testing.T) {
	e, api, errBuf := teleEnv(t)
	if err := e.run(t, "doctor"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errBuf.String(), "Dojo shares usage stats") || !strings.Contains(errBuf.String(), "dojo telemetry off") {
		t.Fatalf("notice missing on stderr: %q", errBuf)
	}
	if strings.Contains(e.out.String(), "usage stats") {
		t.Fatalf("notice leaked to stdout: %q", e.out)
	}
	if n := len(api.posts()); n != 0 {
		t.Fatalf("sent %d requests before the notice was shown on a prior command", n)
	}
	if q, _ := state.LoadQueue(); len(q.Events) != 0 {
		t.Fatalf("queued before the notice: %+v", q)
	}

	errBuf.Reset()
	if err := e.run(t, "doctor"); err != nil {
		t.Fatal(err)
	}
	if errBuf.Len() != 0 {
		t.Fatalf("the notice repeated: %q", errBuf)
	}
	posts := api.posts()
	if len(posts) != 1 {
		t.Fatalf("second command sent %d requests", len(posts))
	}
	b := decodeBody(t, posts[0])
	if len(b.Events) != 1 || b.Events[0].Command != "doctor" || b.Events[0].Result != "ok" {
		t.Fatalf("events %+v", b.Events)
	}
	if b.OS != runtime.GOOS+"/"+runtime.GOARCH || b.CLIVersion != "dev" {
		t.Fatalf("os/version %q %q", b.OS, b.CLIVersion)
	}
	if posts[0].auth != "" {
		t.Fatalf("anonymous send carried %q", posts[0].auth)
	}
}

func TestNoNoticeWithoutStderrOrAPI(t *testing.T) {
	e, api, _ := teleEnv(t)
	e.app.Err = nil
	_ = e.run(t, "doctor")
	_ = e.run(t, "doctor")
	if len(api.posts()) != 0 {
		t.Fatal("sent without ever being able to show the notice")
	}
	e, api, errBuf := teleEnv(t)
	e.app.Login = LoginConfig{}
	_ = e.run(t, "doctor")
	_ = e.run(t, "doctor")
	if len(api.posts()) != 0 || errBuf.Len() != 0 {
		t.Fatalf("no API configured must be silent: %d posts, notice %q", len(api.posts()), errBuf)
	}
}

func TestNothingSentWhenOff(t *testing.T) {
	e, api, errBuf := teleEnv(t)
	if err := e.run(t, "telemetry", "off"); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		_ = e.run(t, "doctor")
	}
	if len(api.posts()) != 0 || errBuf.Len() != 0 {
		t.Fatalf("off: %d posts, stderr %q", len(api.posts()), errBuf)
	}
	if q, _ := state.LoadQueue(); len(q.Events) != 0 {
		t.Fatalf("off queued events: %+v", q)
	}
}

func TestOffDropsTheQueue(t *testing.T) {
	e, _, _ := teleEnv(t)
	shown(t)
	_ = state.SaveQueue(state.Queue{Events: []state.Event{{Command: "doctor", Result: "ok"}}})
	if err := e.run(t, "telemetry", "off"); err != nil {
		t.Fatal(err)
	}
	if q, _ := state.LoadQueue(); len(q.Events) != 0 {
		t.Fatalf("queue survived off: %+v", q)
	}
}

func TestEnvOverrideAlwaysWins(t *testing.T) {
	for _, tt := range []struct{ k, v string }{
		{"DOJO_TELEMETRY", "off"}, {"DOJO_TELEMETRY", "0"}, {"DOJO_TELEMETRY", "FALSE"}, {"DO_NOT_TRACK", "1"}, {"DO_NOT_TRACK", "true"},
	} {
		t.Run(tt.k+"="+tt.v, func(t *testing.T) {
			e, api, errBuf := teleEnv(t)
			shown(t) // switch on, notice shown: only the environment says no
			e.app.Getenv = func(k string) string {
				if k == tt.k {
					return tt.v
				}
				return ""
			}
			_ = e.run(t, "doctor")
			_ = e.run(t, "doctor")
			if len(api.posts()) != 0 || errBuf.Len() != 0 {
				t.Fatalf("env %s=%s: %d posts, stderr %q", tt.k, tt.v, len(api.posts()), errBuf)
			}
			_ = e.run(t, "telemetry", "status")
			if !strings.Contains(e.out.String(), "off ("+tt.k+" is set") {
				t.Fatalf("status: %q", e.out)
			}
		})
	}
	// DO_NOT_TRACK=0 and an unset variable do not switch anything off.
	e, api, _ := teleEnv(t)
	shown(t)
	e.app.Getenv = func(k string) string {
		if k == "DO_NOT_TRACK" {
			return "0"
		}
		return ""
	}
	_ = e.run(t, "doctor")
	if len(api.posts()) != 1 {
		t.Fatal("DO_NOT_TRACK=0 must not disable")
	}
}

func TestEventsCarryNamesOnly(t *testing.T) {
	e, api, _ := teleEnv(t)
	shown(t)
	for _, args := range [][]string{
		{"start", "white/a"},
		{"start", "--key", "/secret/keys/id_ed25519", "white/a"},
		{"check", "--quick"},
		{"login", "--api-url", api.srv.URL + "/private-path"},
	} {
		_ = e.run(t, args...)
	}
	allowed := map[string]bool{"start": true, "check": true, "login": true}
	seen := 0
	for _, p := range api.posts() {
		for _, bad := range []string{"white/a", "secret", "id_ed25519", "--quick", "--key", "private-path", "Kata A", "setup output"} {
			if strings.Contains(p.body, bad) {
				t.Fatalf("body contains %q: %s", bad, p.body)
			}
		}
		for _, ev := range decodeBody(t, p).Events {
			seen++
			if !allowed[ev.Command] || (ev.Result != "ok" && ev.Result != "error" && ev.Result != "failed") {
				t.Fatalf("event %+v", ev)
			}
		}
	}
	if seen != 4 {
		t.Fatalf("saw %d events, want 4", seen)
	}
}

func TestResultNames(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tt := range []struct {
		ctx  context.Context
		err  error
		want string
	}{
		{context.Background(), nil, "ok"},
		{context.Background(), ErrCheckFailed, "failed"},
		{context.Background(), ErrNotReady, "failed"},
		{context.Background(), os.ErrNotExist, "error"},
		{canceled, os.ErrNotExist, "canceled"},
	} {
		if got := resultName(tt.ctx, tt.err); got != tt.want {
			t.Errorf("resultName(%v) = %q, want %q", tt.err, got, tt.want)
		}
	}
}

func TestUnknownAndHelpAreNotRecorded(t *testing.T) {
	e, api, _ := teleEnv(t)
	shown(t)
	_ = e.run(t)
	_ = e.run(t, "frobnicate", "--secret")
	_ = e.run(t, "mcp", "--bogus")
	_ = e.run(t, "telemetry", "status")
	if len(api.posts()) != 0 {
		t.Fatalf("recorded %d requests for help, a typo, mcp or telemetry", len(api.posts()))
	}
}

func TestBatchesOfTwenty(t *testing.T) {
	e, api, _ := teleEnv(t)
	shown(t)
	var q state.Queue
	for range 45 {
		q.Events = append(q.Events, state.Event{Command: "doctor", Result: "ok", DurationMS: 5})
	}
	_ = state.SaveQueue(q)
	_ = e.run(t, "doctor") // adds a 46th event
	posts := api.posts()
	var sizes []int
	for _, p := range posts {
		sizes = append(sizes, len(decodeBody(t, p).Events))
	}
	if len(sizes) != 3 || sizes[0] != 20 || sizes[1] != 20 || sizes[2] != 6 {
		t.Fatalf("batch sizes %v, want [20 20 6]", sizes)
	}
	if left, _ := state.LoadQueue(); len(left.Events) != 0 {
		t.Fatalf("queue not emptied: %d left", len(left.Events))
	}
}

func TestFailuresKeepTheQueueAndNeverFailTheCommand(t *testing.T) {
	for _, tt := range []struct {
		name       string
		status     int
		retryAfter string
		wantKept   bool
		wantWait   time.Duration
	}{
		{"429 with Retry-After", 429, "120", true, 120 * time.Second},
		{"429 huge Retry-After is capped", 429, "999999", true, time.Hour},
		{"500", 500, "", true, time.Minute},
		{"400 is dropped", 400, "", false, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e, api, _ := teleEnv(t)
			shown(t)
			api.status, api.retryAfter = tt.status, tt.retryAfter
			if err := e.run(t, "doctor"); err != nil {
				t.Fatalf("a telemetry failure failed the command: %v", err)
			}
			q, _ := state.LoadQueue()
			if (len(q.Events) == 1) != tt.wantKept {
				t.Fatalf("queue %+v, kept=%v wanted", q, tt.wantKept)
			}
			if tt.wantWait > 0 {
				if got := q.RetryAfter.Sub(e.app.Now()); got != tt.wantWait {
					t.Fatalf("backoff %v, want %v", got, tt.wantWait)
				}
				before := len(api.posts())
				_ = e.run(t, "doctor") // clock is fixed: still inside the backoff
				if len(api.posts()) != before {
					t.Fatal("sent during the backoff")
				}
				if q, _ := state.LoadQueue(); len(q.Events) != 2 {
					t.Fatalf("event lost during the backoff: %+v", q)
				}
				api.status = 204
				later := e.app.Now().Add(tt.wantWait + time.Second)
				e.app.Now = func() time.Time { return later }
				_ = e.run(t, "doctor")
				if q, _ := state.LoadQueue(); len(q.Events) != 0 || !q.RetryAfter.IsZero() {
					t.Fatalf("queue after the backoff: %+v", q)
				}
			}
		})
	}
}

func TestUnreachableAPIKeepsTheQueue(t *testing.T) {
	e, api, _ := teleEnv(t)
	shown(t)
	api.srv.Close()
	if err := e.run(t, "doctor"); err != nil {
		t.Fatal(err)
	}
	if q, _ := state.LoadQueue(); len(q.Events) != 1 || q.RetryAfter.IsZero() {
		t.Fatalf("queue %+v", q)
	}
}

func TestSlowAPIDoesNotHoldTheCommand(t *testing.T) {
	e, api, _ := teleEnv(t)
	shown(t)
	api.block = make(chan struct{})
	t.Cleanup(func() { close(api.block) })
	t0 := time.Now()
	err := e.run(t, "doctor")
	if err != nil {
		t.Fatalf("command failed: %v", err)
	}
	if d := time.Since(t0); d > telemetryTimeout+time.Second {
		t.Fatalf("command held %v by a slow API", d)
	}
	if q, _ := state.LoadQueue(); len(q.Events) != 1 {
		t.Fatalf("queue %+v", q)
	}
}

func TestNoticeIsHonest(t *testing.T) {
	if strings.Contains(strings.ToLower(telemetryNotice), "anonymous") || strings.Contains(strings.ToLower(telemetryWhat), "anonymous") {
		t.Fatal("the stats are tied to the account when logged in: do not call them anonymous")
	}
	if !strings.Contains(telemetryNotice, "tied to your account") || !strings.Contains(telemetryNotice, "dojo telemetry off") {
		t.Fatalf("notice %q", telemetryNotice)
	}
	if strings.Contains(strings.ToLower(Usage), "anonymous") {
		t.Fatal("help calls the stats anonymous")
	}
}

func TestBearerRejectedDropsTheBatchNeverResendsAnonymously(t *testing.T) {
	e, api, _ := teleEnv(t)
	shown(t)
	if err := state.SaveToken(dojoToken); err != nil {
		t.Fatal(err)
	}
	api.perAuth = map[string]int{"Bearer " + dojoToken: 401}
	if err := e.run(t, "doctor"); err != nil {
		t.Fatal(err)
	}
	posts := api.posts()
	if len(posts) != 1 || posts[0].auth != "Bearer "+dojoToken {
		t.Fatalf("posts %+v, want one bearer request and no anonymous resend", posts)
	}
	if tokenOnDisk(t) != dojoToken {
		t.Fatal("a rejected telemetry bearer logged the learner out")
	}
	if q, _ := state.LoadQueue(); len(q.Events) != 0 || q.RetryAfter.IsZero() {
		t.Fatalf("the rejected batch must be dropped and sending held: %+v", q)
	}
	_ = e.run(t, "doctor") // still inside the hold: nothing goes out, certainly not anonymously
	if len(api.posts()) != 1 {
		t.Fatalf("sent during the hold: %+v", api.posts())
	}
}

func TestLoggedInSendsBearer(t *testing.T) {
	e, api, _ := teleEnv(t)
	shown(t)
	_ = state.SaveToken(dojoToken)
	_ = e.run(t, "doctor")
	if p := api.posts(); len(p) != 1 || p[0].auth != "Bearer "+dojoToken {
		t.Fatalf("posts %+v", p)
	}
}

func TestQueueFileIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no unix modes")
	}
	e, api, _ := teleEnv(t)
	shown(t)
	api.status = 500
	_ = e.run(t, "doctor")
	p := filepath.Join(os.Getenv("DOJO_HOME"), "telemetry-queue.json")
	fi, err := os.Stat(p)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("queue file %v %v", fi, err)
	}
}

func TestTelemetryOffMirrorsToAccount(t *testing.T) {
	e, api, _ := teleEnv(t)
	_ = state.SaveToken(dojoToken)
	if err := e.run(t, "telemetry", "off"); err != nil {
		t.Fatal(err)
	}
	p := api.patches()
	if len(p) != 1 || p[0].auth != "Bearer "+dojoToken || p[0].body != `{"usage_stats":false}` {
		t.Fatalf("patches %+v", p)
	}
	if !strings.Contains(e.out.String(), "account setting is off too") {
		t.Fatalf("output %q", e.out)
	}
	if err := e.run(t, "telemetry", "on"); err != nil {
		t.Fatal(err)
	}
	if p := api.patches(); len(p) != 2 || p[1].body != `{"usage_stats":true}` {
		t.Fatalf("patches %+v", p)
	}
}

func TestTelemetryOffLoggedOutDoesNotCallTheAPI(t *testing.T) {
	e, api, _ := teleEnv(t)
	if err := e.run(t, "telemetry", "off"); err != nil {
		t.Fatal(err)
	}
	if len(api.patches()) != 0 {
		t.Fatal("PATCH without a login")
	}
	if st, _ := state.LoadTelemetry(); !st.Off {
		t.Fatal("not off")
	}
}

func TestTelemetryOffKeepsLocalSwitchWhenAPIFails(t *testing.T) {
	for name, tweak := range map[string]func(*teleAPI){
		"unreachable": func(a *teleAPI) { a.srv.Close() },
		"401":         func(a *teleAPI) { a.patch = 401 },
		"500":         func(a *teleAPI) { a.patch = 500 },
	} {
		t.Run(name, func(t *testing.T) {
			e, api, _ := teleEnv(t)
			_ = state.SaveToken(dojoToken)
			tweak(api)
			if err := e.run(t, "telemetry", "off"); err != nil {
				t.Fatalf("local off must succeed: %v", err)
			}
			if st, _ := state.LoadTelemetry(); !st.Off {
				t.Fatal("local switch not off")
			}
			if !strings.Contains(e.out.String(), "Could not set usage stats off on your account") ||
				!strings.Contains(e.out.String(), "This computer keeps its setting") {
				t.Fatalf("output %q", e.out)
			}
			if tokenOnDisk(t) != dojoToken {
				t.Fatal("token touched")
			}
		})
	}
}

func TestTelemetryCommandUsageAndStatus(t *testing.T) {
	e, _, _ := teleEnv(t)
	if err := e.run(t, "telemetry", "maybe"); err == nil || !strings.Contains(err.Error(), "usage: dojo telemetry") {
		t.Fatalf("err %v", err)
	}
	if err := e.run(t, "telemetry", "on", "off"); err == nil {
		t.Fatal("two arguments accepted")
	}
	if err := e.run(t, "telemetry"); err != nil || !strings.Contains(e.out.String(), "Usage stats: on") ||
		!strings.Contains(e.out.String(), "Never") {
		t.Fatalf("status: %v %q", err, e.out)
	}
	if st, _ := state.LoadTelemetry(); !st.NoticeShown {
		t.Fatal("status counts as the notice")
	}
	_ = e.run(t, "telemetry", "off")
	_ = e.run(t, "telemetry", "status")
	if !strings.Contains(e.out.String(), "Usage stats: off") {
		t.Fatalf("status after off: %q", e.out)
	}
}

func TestCorruptSwitchFileSendsNothing(t *testing.T) {
	e, api, errBuf := teleEnv(t)
	home := os.Getenv("DOJO_HOME")
	_ = os.MkdirAll(home, 0o700)
	_ = os.WriteFile(filepath.Join(home, "telemetry.json"), []byte("{nope"), 0o600)
	_ = e.run(t, "doctor")
	_ = e.run(t, "doctor")
	if len(api.posts()) != 0 || errBuf.Len() != 0 {
		t.Fatalf("corrupt settings: %d posts, %q", len(api.posts()), errBuf)
	}
}

func TestLoginAlignsSwitchFromAccount(t *testing.T) {
	s := newStack(t)
	s.polls = []map[string]any{granted()}
	s.meBody = `{"username":"ada","github_login":"ada","usage_stats":false}`
	e, _ := loginEnv(t, s)
	e.app.Err = &bytes.Buffer{}
	e.app.Getenv = func(string) string { return "" }
	shown(t)
	_ = state.SaveQueue(state.Queue{Events: []state.Event{{Command: "doctor", Result: "ok"}}})
	if err := e.run(t, "login"); err != nil {
		t.Fatal(err)
	}
	if st, _ := state.LoadTelemetry(); !st.Off {
		t.Fatal("server off did not turn the local switch off")
	}
	if q, _ := state.LoadQueue(); len(q.Events) != 0 {
		t.Fatalf("queue kept after the account said off: %+v", q)
	}
	if !strings.Contains(e.out.String(), "off on your account") {
		t.Fatalf("output %q", e.out)
	}
}

func TestLoginWithAccountOnLeavesSwitchAlone(t *testing.T) {
	for _, local := range []bool{false, true} {
		s := newStack(t)
		s.polls = []map[string]any{granted()}
		s.meBody = `{"username":"ada","github_login":"ada","usage_stats":true}`
		e, _ := loginEnv(t, s)
		e.app.Getenv = func(string) string { return "" }
		_ = state.SaveTelemetry(state.Telemetry{Off: local, NoticeShown: true})
		if err := e.run(t, "login"); err != nil {
			t.Fatal(err)
		}
		if st, _ := state.LoadTelemetry(); st.Off != local {
			t.Fatalf("local %v became %v", local, st.Off)
		}
	}
}

func TestLoginDoesNotSendWhenItSwitchedOff(t *testing.T) {
	api := newTeleAPI(t)
	s := newStack(t)
	s.polls = []map[string]any{granted()}
	s.meBody = `{"username":"ada","usage_stats":false}`
	e, _ := loginEnv(t, s)
	e.app.Login.APIURL = s.srv.URL
	e.app.Getenv = func(string) string { return "" }
	shown(t)
	_ = e.run(t, "login")
	if len(api.posts()) != 0 {
		t.Fatal("unrelated api hit")
	}
	if q, _ := state.LoadQueue(); len(q.Events) != 0 {
		t.Fatalf("login recorded itself after switching off: %+v", q)
	}
}
