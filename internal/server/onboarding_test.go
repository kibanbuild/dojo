package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kibanbuild/dojo/internal/server/repo"
)

func id(b byte) pgtype.UUID { return pgtype.UUID{Bytes: [16]byte{b}, Valid: true} }

// choiceRig is the sign-in rig plus the paths the catalog would hold and a session for signed-in calls.
type choiceRig struct {
	*rig
	sess *fakeSessions
}

func newChoiceRig(t *testing.T) *choiceRig {
	t.Helper()
	r := newRig(t)
	r.st.paths = map[string]repo.GetPathBySlugRow{
		"systems-devops": {ID: id(1), Slug: "systems-devops", Status: "available"},
		"security":       {ID: id(2), Slug: "security", Status: "planned"},
		"backend":        {ID: id(3), Slug: "backend", Status: "planned"},
		"frontend":       {ID: id(4), Slug: "frontend", Status: "planned"},
		"mobile":         {ID: id(5), Slug: "mobile", Status: "planned"},
	}
	sess := &fakeSessions{}
	r.h = New(fakeReady{}, Auth{Store: sess}, NewRateLimits(1), r.si, nil)
	return &choiceRig{rig: r, sess: sess}
}

type choiceCall struct {
	method, target, body string
	cookies              []*http.Cookie
	signedIn, suspended  bool
	noCSRF               bool
	ctype                string
}

func (c *choiceRig) do(cc choiceCall) *httptest.ResponseRecorder {
	req := httptest.NewRequest(cc.method, cc.target, strings.NewReader(cc.body))
	req.Header.Set("Content-Type", "application/json")
	if cc.ctype != "" {
		req.Header.Set("Content-Type", cc.ctype)
	}
	if !cc.noCSRF {
		req.Header.Set(CSRFHeader, "1")
	}
	for _, k := range cc.cookies {
		req.AddCookie(k)
	}
	if cc.signedIn {
		c.sess.row = activeRow(time.Now(), cc.suspended)
		req.AddCookie(&http.Cookie{Name: SessionCookie, Value: goodToken})
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	return rec
}

func pathSetCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == pathCookieName {
			return c
		}
	}
	return nil
}

func TestPathCookieAttributesAndSignature(t *testing.T) {
	r := newChoiceRig(t)
	rec := r.do(choiceCall{method: "POST", target: pathChoicePath, body: `{"path":"systems-devops"}`})
	c := pathSetCookie(rec)
	if rec.Code != http.StatusNoContent || c == nil || c.Path != "/v1" || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.MaxAge != 86400 || c.Secure {
		t.Fatalf("got %d %+v", rec.Code, c)
	}
	r.si.Auth.Secure = true
	if c := pathSetCookie(r.do(choiceCall{method: "POST", target: pathChoicePath, body: `{"path":"systems-devops"}`})); !c.Secure {
		t.Fatal("Secure missing for an https origin")
	}
	r.si.Auth.Secure = false
	got, ok := r.si.readPathCookie(requestWith(c))
	if !ok || got.Path != "systems-devops" || got.V != 1 {
		t.Fatalf("round trip: %+v %v", got, ok)
	}
	// The state cookie's MAC label is another one: a state cookie never passes as a path cookie.
	st := r.si.seal(oauthState{V: 1, State: "s", Verifier: "v", Issued: r.t0.Unix()})
	if _, ok := r.si.readPathCookie(requestWith(&http.Cookie{Name: pathCookieName, Value: st})); ok {
		t.Fatal("a state cookie passed as a path cookie")
	}
}

func requestWith(c *http.Cookie) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(c)
	return req
}

func TestPathCookieIgnoredWhenInvalid(t *testing.T) {
	r := newChoiceRig(t)
	good := r.si.sealPath(pathChoice{V: 1, Path: "systems-devops", Issued: r.t0.Unix()})
	tests := []struct {
		name, value string
		age         time.Duration
	}{
		{"tampered", "x" + good[1:], 0},
		{"no signature", strings.SplitN(good, ".", 2)[0], 0},
		{"other version", r.si.sealPath(pathChoice{V: 2, Path: "systems-devops", Issued: r.t0.Unix()}), 0},
		{"four on the waitlist", r.si.sealPath(pathChoice{V: 1, Waitlist: []string{"a", "b", "c", "d"}, Issued: r.t0.Unix()}), 0},
		{"not a slug", r.si.sealPath(pathChoice{V: 1, Path: "Bad Slug", Issued: r.t0.Unix()}), 0},
		{"expired", good, 24*time.Hour + time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r.t0 = time.Unix(1_800_000_000, 0)
			r.si.Now = func() time.Time { return r.t0.Add(tt.age) }
			if _, ok := r.si.readPathCookie(requestWith(&http.Cookie{Name: pathCookieName, Value: tt.value})); ok {
				t.Fatal("accepted")
			}
		})
	}
	r.si.Now = func() time.Time { return r.t0.Add(24 * time.Hour) }
	if _, ok := r.si.readPathCookie(requestWith(&http.Cookie{Name: pathCookieName, Value: good})); !ok {
		t.Fatal("rejected at exactly 24 hours")
	}
}

func TestOnboardingWritesSignedOut(t *testing.T) {
	r := newChoiceRig(t)
	c := func(rec *httptest.ResponseRecorder) []*http.Cookie { return []*http.Cookie{pathSetCookie(rec)} }
	rec := r.do(choiceCall{method: "POST", target: pathWaitlistPath, body: `{"path":"security"}`})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("waitlist: %d %s", rec.Code, rec.Body)
	}
	jar := c(rec)
	rec = r.do(choiceCall{method: "POST", target: pathChoicePath, body: `{"path":"systems-devops"}`, cookies: jar})
	got, _ := r.si.readPathCookie(requestWith(pathSetCookie(rec)))
	if got.Path != "systems-devops" || len(got.Waitlist) != 1 || got.Waitlist[0] != "security" {
		t.Fatalf("path keeps the waitlist: %+v", got)
	}
	jar = c(rec)
	// Idempotent, then up to three.
	for _, s := range []string{"security", "backend", "frontend"} {
		rec = r.do(choiceCall{method: "POST", target: pathWaitlistPath, body: `{"path":"` + s + `"}`, cookies: jar})
		if rec.Code != http.StatusNoContent {
			t.Fatalf("add %s: %d", s, rec.Code)
		}
		jar = c(rec)
	}
	got, _ = r.si.readPathCookie(requestWith(jar[0]))
	if len(got.Waitlist) != 3 {
		t.Fatalf("waitlist: %+v", got)
	}
	if rec = r.do(choiceCall{method: "POST", target: pathWaitlistPath, body: `{"path":"mobile"}`, cookies: jar}); rec.Code != http.StatusBadRequest {
		t.Fatalf("fourth: %d", rec.Code)
	}
	rec = r.do(choiceCall{method: "DELETE", target: pathWaitlistPath + "/backend", cookies: jar})
	got, _ = r.si.readPathCookie(requestWith(pathSetCookie(rec)))
	if rec.Code != http.StatusNoContent || len(got.Waitlist) != 2 || got.Path != "systems-devops" {
		t.Fatalf("delete: %d %+v", rec.Code, got)
	}
	if len(r.st.chosen)+len(r.st.listed)+len(r.st.unlisted) != 0 {
		t.Fatal("a signed-out call wrote to the database")
	}
}

func TestOnboardingWritesSignedIn(t *testing.T) {
	r := newChoiceRig(t)
	rec := r.do(choiceCall{method: "POST", target: pathChoicePath, body: `{"path":"systems-devops"}`, signedIn: true})
	if rec.Code != http.StatusNoContent || len(r.st.chosen) != 1 || r.st.chosen[0].ChosenPathID != id(1) || pathSetCookie(rec) != nil {
		t.Fatalf("path: %d %+v", rec.Code, r.st.chosen)
	}
	rec = r.do(choiceCall{method: "POST", target: pathWaitlistPath, body: `{"path":"security"}`, signedIn: true})
	if rec.Code != http.StatusNoContent || len(r.st.listed) != 1 || r.st.listed[0].PathID != id(2) {
		t.Fatalf("waitlist: %d %+v", rec.Code, r.st.listed)
	}
	rec = r.do(choiceCall{method: "DELETE", target: pathWaitlistPath + "/security", signedIn: true})
	if rec.Code != http.StatusNoContent || len(r.st.unlisted) != 1 || r.st.unlisted[0].PathID != id(2) {
		t.Fatalf("delete: %d %+v", rec.Code, r.st.unlisted)
	}
}

func TestOnboardingWriteErrors(t *testing.T) {
	r := newChoiceRig(t)
	tests := []struct {
		name string
		call choiceCall
		code int
		body string
	}{
		{"no csrf header", choiceCall{method: "POST", target: pathChoicePath, body: `{"path":"systems-devops"}`, noCSRF: true}, 403, "csrf_rejected"},
		{"no csrf header, signed in", choiceCall{method: "POST", target: pathChoicePath, body: `{"path":"systems-devops"}`, noCSRF: true, signedIn: true}, 403, "csrf_rejected"},
		{"form content type", choiceCall{method: "POST", target: pathChoicePath, body: `path=x`, ctype: "application/x-www-form-urlencoded"}, 403, "csrf_rejected"},
		{"delete without csrf", choiceCall{method: "DELETE", target: pathWaitlistPath + "/security", noCSRF: true}, 403, "csrf_rejected"},
		{"not json", choiceCall{method: "POST", target: pathChoicePath, body: `nope`}, 400, "bad_request"},
		{"unknown field", choiceCall{method: "POST", target: pathChoicePath, body: `{"path":"systems-devops","x":1}`}, 400, "bad_request"},
		{"not a slug", choiceCall{method: "POST", target: pathChoicePath, body: `{"path":"Nope Nope"}`}, 400, "bad_request"},
		{"empty", choiceCall{method: "POST", target: pathChoicePath, body: `{}`}, 400, "bad_request"},
		{"unknown path", choiceCall{method: "POST", target: pathChoicePath, body: `{"path":"nope"}`}, 404, "not_found"},
		{"planned path", choiceCall{method: "POST", target: pathChoicePath, body: `{"path":"security"}`}, 409, "path_planned"},
		{"waitlist unknown", choiceCall{method: "POST", target: pathWaitlistPath, body: `{"path":"nope"}`}, 404, "not_found"},
		{"waitlist of an available path", choiceCall{method: "POST", target: pathWaitlistPath, body: `{"path":"systems-devops"}`}, 409, "path_available"},
		{"delete unknown", choiceCall{method: "DELETE", target: pathWaitlistPath + "/nope"}, 404, "not_found"},
		{"delete available", choiceCall{method: "DELETE", target: pathWaitlistPath + "/systems-devops"}, 409, "path_available"},
		{"suspended", choiceCall{method: "POST", target: pathChoicePath, body: `{"path":"systems-devops"}`, signedIn: true, suspended: true}, 403, "account_suspended"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := r.do(tt.call)
			if rec.Code != tt.code || !strings.Contains(rec.Body.String(), `"`+tt.body+`"`) {
				t.Fatalf("got %d %q", rec.Code, rec.Body)
			}
			if len(r.st.chosen)+len(r.st.listed)+len(r.st.unlisted) != 0 || pathSetCookie(rec) != nil {
				t.Fatal("a refused call changed something")
			}
		})
	}
}

func TestOnboardingWritesAreRateLimited(t *testing.T) {
	r := newChoiceRig(t)
	// Signed out: the per-IP auth budget of 30.
	limited := 0
	for i := 0; i < 35; i++ {
		if r.do(choiceCall{method: "POST", target: pathChoicePath, body: `{"path":"systems-devops"}`}).Code == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited != 5 {
		t.Fatalf("signed out: %d of 35 limited, want 5", limited)
	}
	// Signed in: the shared learner budget of 10 writes, across the three routes.
	r = newChoiceRig(t)
	limited = 0
	for i := 0; i < 15; i++ {
		target, body := pathChoicePath, `{"path":"systems-devops"}`
		if i%3 == 1 {
			target, body = pathWaitlistPath, `{"path":"security"}`
		}
		rec := r.do(choiceCall{method: "POST", target: target, body: body, signedIn: true})
		if rec.Code == http.StatusTooManyRequests {
			limited++
			if rec.Header().Get("Retry-After") == "" {
				t.Fatal("no Retry-After")
			}
		}
	}
	if limited != 5 {
		t.Fatalf("signed in: %d of 15 limited, want 5", limited)
	}
}

func TestStartCopiesTheVerifiedPathCookieIntoTheState(t *testing.T) {
	r := newChoiceRig(t)
	good := r.si.sealPath(pathChoice{V: 1, Path: "systems-devops", Waitlist: []string{"security"}, Issued: r.t0.Unix()})
	start := func(cookie string) oauthState {
		req := httptest.NewRequest(http.MethodPost, startPath, strings.NewReader(bothTicks))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(&http.Cookie{Name: pathCookieName, Value: cookie})
		rec := httptest.NewRecorder()
		r.h.ServeHTTP(rec, req)
		st, ok := r.si.open(rec.Result().Cookies()[0].Value)
		if !ok {
			t.Fatal("no state")
		}
		return st
	}
	if st := start(good); st.Path != "systems-devops" || len(st.Waitlist) != 1 || st.Waitlist[0] != "security" {
		t.Fatalf("state: %+v", st)
	}
	if st := start("x" + good[1:]); st.Path != "" || len(st.Waitlist) != 0 {
		t.Fatalf("a tampered cookie reached the state: %+v", st)
	}
}

// The callback trusts the sealed state only: a dojo_path cookie sent with the callback changes nothing.
func TestCallbackAppliesTheStateChoice(t *testing.T) {
	r := newChoiceRig(t)
	good := r.si.sealPath(pathChoice{V: 1, Path: "systems-devops", Waitlist: []string{"security"}, Issued: r.t0.Unix()})
	swapped := r.si.sealPath(pathChoice{V: 1, Path: "other", Issued: r.t0.Unix()})
	req := httptest.NewRequest(http.MethodPost, startPath, strings.NewReader(bothTicks))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: pathCookieName, Value: good})
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	state := rec.Result().Cookies()[0]
	loc := rec.Header().Get("Location")
	q := loc[strings.Index(loc, "state=")+len("state="):]
	q = strings.SplitN(q, "&", 2)[0]

	cb := httptest.NewRequest(http.MethodGet, callbackPath+"?code=c&state="+q, nil)
	cb.AddCookie(state)
	cb.AddCookie(&http.Cookie{Name: pathCookieName, Value: swapped})
	out := httptest.NewRecorder()
	r.h.ServeHTTP(out, cb)
	if out.Header().Get("Location") != "/app" || len(r.st.applies) != 1 {
		t.Fatalf("callback: %q applies=%d", out.Header().Get("Location"), len(r.st.applies))
	}
	a := r.st.applies[0]
	if a.Path.String != "systems-devops" || len(a.Waitlist) != 1 || a.Waitlist[0] != "security" {
		t.Fatalf("applied: %+v", a)
	}
	if c := pathSetCookie(out); c == nil || c.MaxAge >= 0 || c.Path != "/v1" {
		t.Fatalf("dojo_path not deleted: %+v", c)
	}
}

func TestCallbackWithoutAChoiceAppliesNothing(t *testing.T) {
	r := newChoiceRig(t)
	c, state := r.begin(t, startPath, bothTicks)
	if rec := r.callback(c, "code=c&state="+state); rec.Header().Get("Location") != "/app" || len(r.st.applies) != 0 {
		t.Fatalf("got %q applies=%d", rec.Header().Get("Location"), len(r.st.applies))
	}
}

func TestCallbackApplyFailureKeepsTheCookieAndEndsBeforeASession(t *testing.T) {
	r := newChoiceRig(t)
	good := r.si.sealPath(pathChoice{V: 1, Path: "systems-devops", Issued: r.t0.Unix()})
	req := httptest.NewRequest(http.MethodPost, startPath, strings.NewReader(bothTicks))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: pathCookieName, Value: good})
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	loc := rec.Header().Get("Location")
	q := strings.SplitN(loc[strings.Index(loc, "state=")+6:], "&", 2)[0]
	r.st.applyErr = http.ErrAbortHandler
	cb := httptest.NewRequest(http.MethodGet, callbackPath+"?code=c&state="+q, nil)
	cb.AddCookie(rec.Result().Cookies()[0])
	out := httptest.NewRecorder()
	r.h.ServeHTTP(out, cb)
	if out.Header().Get("Location") != "/app/signin?error=server_error" || r.st.sessions != 0 || pathSetCookie(out) != nil {
		t.Fatalf("got %q sessions=%d", out.Header().Get("Location"), r.st.sessions)
	}
}
