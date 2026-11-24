package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/kibanbuild/dojo/internal/state"
)

type eventsAPI struct {
	srv        *httptest.Server
	mu         sync.Mutex
	bodies     []string
	auths      []string
	status     int
	retryAfter string
	version    string // catalog_version in the bundle answer
}

func newEventsAPI(t *testing.T) *eventsAPI {
	t.Helper()
	a := &eventsAPI{status: 200, version: "a1b2c3d4e5f6"}
	b := testBundle(t, testCatalog, "white/a")
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && (r.URL.Path == "/v1/tickets/white/a/bundle" || r.URL.Path == "/dl/white/a") {
			a.mu.Lock()
			version := a.version
			a.mu.Unlock()
			serveBundle(w, r, b, version, "http://"+r.Host+"/dl/white/a")
			return
		}
		raw, _ := io.ReadAll(r.Body)
		a.mu.Lock()
		defer a.mu.Unlock()
		if r.Method != http.MethodPost || r.URL.Path != "/v1/events" {
			w.WriteHeader(404)
			return
		}
		a.bodies = append(a.bodies, string(raw))
		a.auths = append(a.auths, r.Header.Get("Authorization"))
		if a.retryAfter != "" {
			w.Header().Set("Retry-After", a.retryAfter)
		}
		w.WriteHeader(a.status)
		_, _ = w.Write([]byte(`{"accepted":1,"duplicates":0,"ignored":0}`))
	}))
	t.Cleanup(a.srv.Close)
	return a
}

func (a *eventsAPI) sent() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.bodies...)
}

func eventsEnv(t *testing.T, loggedIn bool) (*env, *eventsAPI) {
	t.Helper()
	api := newEventsAPI(t)
	e := newEnv(t)
	e.app.Login = LoginConfig{APIURL: api.srv.URL}
	e.app.Getenv = func(string) string { return "" }
	if loggedIn {
		if err := state.SaveToken("tok"); err != nil {
			t.Fatal(err)
		}
	}
	return e, api
}

func writeSidecar(t *testing.T, item, version string) {
	t.Helper()
	dir, err := state.Dir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "bundles"), 0o700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"item_id": item, "sha256": "x", "bytes": 1, "catalog_version": version})
	if err := os.WriteFile(filepath.Join(dir, "bundles", "x.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

type wireEvents struct {
	Events []state.ProgressEvent `json:"events"`
}

func decodeEvents(t *testing.T, body string) []state.ProgressEvent {
	t.Helper()
	var w wireEvents
	if err := json.Unmarshal([]byte(body), &w); err != nil {
		t.Fatalf("body %q: %v", body, err)
	}
	return w.Events
}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestStartStopCheckSendEvents(t *testing.T) {
	e, api := eventsEnv(t, true)
	writeSidecar(t, "white/a", "a1b2c3d4e5f6")
	if err := e.run(t, "start", "white/a"); err != nil {
		t.Fatal(err)
	}
	e.lab.checkCode = 1
	_ = e.run(t, "check")
	e.lab.checkCode = 0
	if err := e.run(t, "check", "--quick"); err != nil { // not a verdict: no event
		t.Fatal(err)
	}
	if err := e.run(t, "stop"); err != nil {
		t.Fatal(err)
	}
	var got []state.ProgressEvent
	for _, b := range api.sent() {
		got = append(got, decodeEvents(t, b)...)
	}
	var types []string
	for _, ev := range got {
		types = append(types, ev.Type)
		if !uuidRe.MatchString(ev.ID) || ev.ItemID != "white/a" {
			t.Errorf("bad event %+v", ev)
		}
		if _, err := time.Parse(time.RFC3339, ev.At); err != nil || !strings.HasSuffix(ev.At, "Z") {
			t.Errorf("at %q", ev.At)
		}
	}
	want := "ticket.started lab.started check.run lab.stopped"
	if strings.Join(types, " ") != want {
		t.Fatalf("types = %v, want %s", types, want)
	}
	if got[0].Lab != "local" || got[0].CatalogVersion != "a1b2c3d4e5f6" {
		t.Errorf("ticket.started = %+v", got[0])
	}
	c := got[2]
	if c.Result != "fail" || c.Passed == nil || *c.Passed != 1 || *c.Total != 2 || c.DurationMS == nil || !strings.Contains(c.Output, "FAIL  two") {
		t.Errorf("check.run = %+v", c)
	}
	for _, a := range api.auths {
		if a != "Bearer tok" {
			t.Errorf("auth = %q", a)
		}
	}
	if q, _ := state.LoadEvents(); len(q.Events) != 0 {
		t.Errorf("queue not emptied: %d", len(q.Events))
	}
}

func TestNoTicketStartedWithoutBundleVersion(t *testing.T) {
	e, api := eventsEnv(t, true)
	api.version = ""
	if err := e.run(t, "start", "white/a"); err != nil {
		t.Fatal(err)
	}
	evs := decodeEvents(t, api.sent()[0])
	if len(evs) != 1 || evs[0].Type != "lab.started" {
		t.Errorf("events = %+v", evs)
	}
}

func TestLabFailedReasons(t *testing.T) {
	e, api := eventsEnv(t, true)
	e.lab.setupCode = 3
	if err := e.run(t, "start", "white/a"); err == nil {
		t.Fatal("setup failure should fail")
	}
	failures := func(bodies []string) (out []state.ProgressEvent) {
		for _, b := range bodies {
			for _, ev := range decodeEvents(t, b) {
				if ev.Type == "lab.failed" {
					out = append(out, ev)
				}
			}
		}
		return out
	}
	evs := failures(api.sent())
	if len(evs) != 1 || evs[0].Reason != "setup" || !strings.Contains(evs[0].Detail, "exited 3") {
		t.Errorf("events = %+v", evs)
	}
	e.lab.recreateErr = io.ErrUnexpectedEOF
	_ = e.run(t, "start", "white/a")
	all := failures(api.sent())[1:]
	if len(all) != 1 || all[0].Reason != "other" {
		t.Errorf("unmapped error should be other: %+v", all)
	}
}

func TestEventsOffForDevCatalogAndNoAPI(t *testing.T) {
	e, api := eventsEnv(t, true)
	e.app.Getenv = func(k string) string {
		if k == "DOJO_KATAS" {
			return "/some/dir"
		}
		return ""
	}
	_ = e.run(t, "start", "white/a")
	if q, _ := state.LoadEvents(); len(q.Events) != 0 || len(api.sent()) != 0 {
		t.Error("DOJO_KATAS must send and queue nothing")
	}
	e2 := newEnv(t)
	_ = e2.run(t, "start", "white/a")
	if q, _ := state.LoadEvents(); len(q.Events) != 0 {
		t.Error("no API: nothing is queued")
	}
}

func TestLoggedOutEventsWaitForLogin(t *testing.T) {
	e, api := eventsEnv(t, true)
	if err := e.run(t, "start", "white/a"); err != nil { // fills the bundle cache
		t.Fatal(err)
	}
	before := len(api.sent())
	if err := state.RemoveToken(); err != nil {
		t.Fatal(err)
	}
	if err := e.run(t, "start", "white/a"); err != nil {
		t.Fatal(err)
	}
	if q, _ := state.LoadEvents(); len(q.Events) != 2 || len(api.sent()) != before {
		t.Fatalf("queued %d, sent %d, want 2 and %d", len(q.Events), len(api.sent()), before)
	}
	if err := state.SaveToken("tok"); err != nil {
		t.Fatal(err)
	}
	e.app.flushEvents(context.Background())
	if len(api.sent()) != before+1 {
		t.Errorf("sent %d", len(api.sent()))
	}
}

func TestFlushStatusHandling(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		retryAfter string
		dropped    bool
		hold       bool
		wait       time.Duration
	}{
		{"200 drops", 200, "", true, false, 0},
		{"400 drops", 400, "", true, false, 0},
		{"429 default minute", 429, "", false, false, time.Minute},
		{"429 retry-after", 429, "120", false, false, 2 * time.Minute},
		{"429 capped at an hour", 429, "99999", false, false, time.Hour},
		{"500 keeps", 500, "", false, false, time.Minute},
		{"401 holds until login", 401, "", false, true, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, api := eventsEnv(t, true)
			api.status, api.retryAfter = tt.status, tt.retryAfter
			e.app.recordEvent(state.ProgressEvent{Type: "lab.stopped", ItemID: "white/a"})
			e.app.flushEvents(context.Background())
			q, _ := state.LoadEvents()
			if (len(q.Events) == 0) != tt.dropped || q.Hold != tt.hold {
				t.Fatalf("events=%d hold=%v", len(q.Events), q.Hold)
			}
			if tt.wait > 0 && !q.RetryAfter.Equal(e.app.now().Add(tt.wait)) {
				t.Errorf("retry_after = %v", q.RetryAfter)
			}
			// A held queue sends nothing more.
			n := len(api.sent())
			e.app.flushEvents(context.Background())
			if !tt.dropped && len(api.sent()) != n {
				t.Error("held queue was sent again")
			}
		})
	}
}

func TestFlushBatches(t *testing.T) {
	e, api := eventsEnv(t, true)
	for range 120 {
		e.app.recordEvent(state.ProgressEvent{Type: "lab.stopped", ItemID: "white/a"})
	}
	e.app.flushEvents(context.Background())
	bodies := api.sent()
	if len(bodies) != 3 {
		t.Fatalf("batches = %d", len(bodies))
	}
	for i, want := range []int{50, 50, 20} {
		if n := len(decodeEvents(t, bodies[i])); n != want {
			t.Errorf("batch %d has %d", i, n)
		}
	}
}

func TestFlushBatchByteLimit(t *testing.T) {
	e, api := eventsEnv(t, true)
	big := strings.Repeat("x", 16*1024)
	for range 60 {
		e.app.recordCheck("white/a", 1, []byte(big), time.Second)
	}
	e.app.flushEvents(context.Background())
	for i, b := range api.sent() {
		if len(b) > 900*1024+64 {
			t.Errorf("batch %d is %d bytes", i, len(b))
		}
	}
	if len(api.sent()) < 2 {
		t.Error("60 events of 16 KB must split across batches")
	}
}

func TestQueueCapDropsOldest(t *testing.T) {
	e, _ := eventsEnv(t, false)
	for range 505 {
		e.app.recordEvent(state.ProgressEvent{Type: "lab.stopped", ItemID: "white/a"})
	}
	q, _ := state.LoadEvents()
	if len(q.Events) != 500 {
		t.Fatalf("len = %d", len(q.Events))
	}
	p, _ := state.Dir()
	fi, err := os.Stat(filepath.Join(p, "events-queue.json"))
	if err != nil || (fi.Mode().Perm()&0o077 != 0 && !isWindows()) {
		t.Errorf("file mode %v err %v", fi, err)
	}
}

func isWindows() bool { return os.PathSeparator == '\\' }

func TestOutputCut(t *testing.T) {
	out := strings.Repeat("é", 20000) // 2 bytes each
	ev := state.ProgressEvent{Output: out}
	got := fitOutput(&ev)
	if len(got) > 16*1024 || !utf8.ValidString(got) || !strings.HasSuffix(out, got) {
		t.Errorf("len %d valid %v", len(got), utf8.ValidString(got))
	}
	// Control characters escape to six bytes each: the event is cut further, tail kept.
	ctl := strings.Repeat("\x01", 16*1024-1) + "TAIL"
	ev = state.ProgressEvent{Type: "check.run", ItemID: "white/a", Output: ctl}
	got = fitOutput(&ev)
	ev.Output = got
	raw, _ := json.Marshal(ev)
	if len(raw) > 64*1024 || !strings.HasSuffix(got, "TAIL") || len(got) >= len(ctl) {
		t.Errorf("event %d bytes, output %d", len(raw), len(got))
	}
}

func TestDetailCappedAtChars(t *testing.T) {
	e, _ := eventsEnv(t, false)
	e.app.recordEvent(state.ProgressEvent{Type: "lab.failed", ItemID: "white/a", Reason: "other", Detail: strings.Repeat("ñ", 900)})
	q, _ := state.LoadEvents()
	if n := utf8.RuneCountInString(q.Events[0].Detail); n != 512 {
		t.Errorf("detail = %d chars", n)
	}
}

func TestLoginAsDifferentAccountDropsQueue(t *testing.T) {
	e, api := eventsEnv(t, true)
	if err := state.SaveAccount("acct-1"); err != nil {
		t.Fatal(err)
	}
	api.status = 401
	e.app.recordEvent(state.ProgressEvent{Type: "lab.stopped", ItemID: "white/a"})
	e.app.flushEvents(context.Background())
	if q, _ := state.LoadEvents(); !q.Hold || len(q.Events) != 1 || q.Account != "acct-1" {
		t.Fatalf("queue = %+v", q)
	}

	// Same account logs in again: the queue is kept and sending resumes.
	e.app.afterLogin(&apiReply{status: 200, body: []byte(`{"id":"acct-1"}`)})
	if q, _ := state.LoadEvents(); q.Hold || len(q.Events) != 1 {
		t.Errorf("same account: %+v", q)
	}
	// A different account: the queue goes first.
	e.app.afterLogin(&apiReply{status: 200, body: []byte(`{"id":"acct-2"}`)})
	if q, _ := state.LoadEvents(); len(q.Events) != 0 {
		t.Errorf("different account kept %d events", len(q.Events))
	}
	if state.LoadAccount() != "acct-2" {
		t.Errorf("account = %q", state.LoadAccount())
	}
}

func TestLoginWithUnknownAccountKeepsQueueHeld(t *testing.T) {
	e, _ := eventsEnv(t, true)
	if err := state.SaveAccount("acct-1"); err != nil {
		t.Fatal(err)
	}
	e.app.recordEvent(state.ProgressEvent{Type: "lab.stopped", ItemID: "white/a"})
	e.app.afterLogin(nil)
	if q, _ := state.LoadEvents(); !q.Hold || len(q.Events) != 1 {
		t.Errorf("queue = %+v", q)
	}
}

func TestEventIDsAreUniqueV7(t *testing.T) {
	now := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)
	seen := map[string]bool{}
	for range 100 {
		id := newEventID(now)
		if !uuidRe.MatchString(id) || seen[id] {
			t.Fatalf("bad or repeated id %q", id)
		}
		seen[id] = true
	}
}

func TestFlushKeepsEventsAppendedDuringSend(t *testing.T) {
	e, api := eventsEnv(t, true)
	e.app.recordEvent(state.ProgressEvent{Type: "lab.stopped", ItemID: "white/a"})
	api.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		e.app.recordEvent(state.ProgressEvent{Type: "lab.started", ItemID: "white/a"}) // another command, meanwhile
		w.WriteHeader(200)
	})
	e.app.flushEvents(context.Background())
	q, _ := state.LoadEvents()
	if len(q.Events) != 1 || q.Events[0].Type != "lab.started" {
		t.Errorf("queue = %+v", q.Events)
	}
}

func TestRecordKeepsFileOnReadErrorButReplacesCorrupt(t *testing.T) {
	e, _ := eventsEnv(t, false)
	dir, _ := state.Dir()
	p := filepath.Join(dir, "events-queue.json")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.app.recordEvent(state.ProgressEvent{Type: "lab.stopped", ItemID: "white/a"})
	if q, err := state.LoadEvents(); err != nil || len(q.Events) != 1 {
		t.Fatalf("corrupt file not replaced: %v %+v", err, q)
	}
	// A read error that is not corruption (the path is a directory) must leave it alone.
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(p, 0o700); err != nil {
		t.Fatal(err)
	}
	e.app.recordEvent(state.ProgressEvent{Type: "lab.stopped", ItemID: "white/a"})
	if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
		t.Errorf("the unreadable path was replaced: %v", err)
	}
}

func TestLoginWithNoIDHoldsAnonymousQueue(t *testing.T) {
	e, _ := eventsEnv(t, true)
	e.app.recordEvent(state.ProgressEvent{Type: "lab.stopped", ItemID: "white/a"})
	if q, _ := state.LoadEvents(); q.Account != "" {
		t.Fatalf("account = %q", q.Account)
	}
	e.app.afterLogin(&apiReply{status: 500})
	if q, _ := state.LoadEvents(); !q.Hold {
		t.Error("queue not held")
	}
}
