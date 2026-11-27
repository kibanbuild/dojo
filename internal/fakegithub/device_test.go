package fakegithub_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/kibanyadev/dojo/internal/fakegithub"
)

const grantType = "urn:ietf:params:oauth:grant-type:device_code"

// newDeviceEnv has a device-enabled client "cid", a web-only client "other", a second device client "dev2".
func newDeviceEnv(t *testing.T, ttl time.Duration) *env {
	e := &env{t: t, now: time.Unix(1_800_000_000, 0)}
	e.srv = httptest.NewServer(fakegithub.New(fakegithub.Config{
		Clients: []fakegithub.Client{
			{ID: "cid", Secret: "sec", RedirectURIs: []string{cbURL}, DeviceFlow: true},
			{ID: "other", Secret: "s2", RedirectURIs: []string{cbURL}},
			{ID: "dev2", Secret: "s3", RedirectURIs: []string{cbURL}, DeviceFlow: true},
		},
		Users: fakegithub.DefaultUsers(), Now: func() time.Time { return e.now }, TokenTTL: ttl,
		PublicURL: "http://fake.test",
	}))
	t.Cleanup(e.srv.Close)
	return e
}

func (e *env) post(path string, form url.Values, accept string) (int, string) {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode, readAll(resp.Body)
}

func (e *env) postJSON(path, client, secret, body string) (int, map[string]any) {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+path, strings.NewReader(body))
	req.SetBasicAuth(client, secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

type device struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

func (e *env) startDevice(client, scope string) device {
	e.t.Helper()
	status, body := e.post("/login/device/code", url.Values{"client_id": {client}, "scope": {scope}}, "application/json")
	var d device
	if status != 200 || json.Unmarshal([]byte(body), &d) != nil || d.DeviceCode == "" {
		e.t.Fatalf("device/code: %d %s", status, body)
	}
	return d
}

func (e *env) poll(client, code string) map[string]any {
	e.t.Helper()
	status, body := e.post("/login/oauth/access_token",
		url.Values{"client_id": {client}, "device_code": {code}, "grant_type": {grantType}}, "application/json")
	if status != 200 {
		e.t.Fatalf("poll: %d, GitHub answers 200 even on errors", status)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		e.t.Fatalf("poll body %q: %v", body, err)
	}
	return out
}

func (e *env) decide(userCode, login, decision string, extra ...string) int {
	e.t.Helper()
	f := url.Values{"user_code": {userCode}, "login": {login}, "decision": {decision}}
	if len(extra) > 0 {
		f.Set("grant", extra[0])
	}
	status, _ := e.post("/login/device", f, "")
	return status
}

// deviceToken runs the whole flow for login and returns the token.
func (e *env) deviceToken(client, login, scope string) string {
	e.t.Helper()
	d := e.startDevice(client, scope)
	if got := e.decide(d.UserCode, login, "approve"); got != 200 {
		e.t.Fatalf("approve: %d", got)
	}
	e.now = e.now.Add(6 * time.Second)
	out := e.poll(client, d.DeviceCode)
	tok, _ := out["access_token"].(string)
	if tok == "" {
		e.t.Fatalf("no token: %v", out)
	}
	return tok
}

func TestDeviceCodeShape(t *testing.T) {
	e := newDeviceEnv(t, 0)
	d := e.startDevice("cid", "read:user user:email")
	if !regexp.MustCompile(`^[A-Z]{4}-[A-Z]{4}$`).MatchString(d.UserCode) {
		t.Errorf("user_code %q", d.UserCode)
	}
	if len(d.DeviceCode) != 40 || d.VerificationURI != "http://fake.test/login/device" || d.ExpiresIn != 900 || d.Interval != 5 {
		t.Errorf("shape: %+v", d)
	}
	// Form-encoded when JSON is not asked for.
	status, body := e.post("/login/device/code", url.Values{"client_id": {"cid"}}, "")
	v, _ := url.ParseQuery(body)
	if status != 200 || v.Get("device_code") == "" || v.Get("expires_in") != "900" || v.Get("interval") != "5" {
		t.Errorf("form reply: %d %s", status, body)
	}
	if d2 := e.startDevice("cid", ""); d2.DeviceCode == d.DeviceCode || d2.UserCode == d.UserCode {
		t.Error("codes repeat")
	}
}

func TestDeviceCodeErrors(t *testing.T) {
	e := newDeviceEnv(t, 0)
	for _, tc := range []struct{ name, client, want string }{
		{"unknown client", "nope", "incorrect_client_credentials"},
		{"flow not enabled", "other", "device_flow_disabled"},
		{"missing client", "", "incorrect_client_credentials"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := e.post("/login/device/code", url.Values{"client_id": {tc.client}}, "application/json")
			var out map[string]any
			_ = json.Unmarshal([]byte(body), &out)
			if status != 200 || out["error"] != tc.want {
				t.Errorf("got %d %s", status, body)
			}
		})
	}
}

func TestDevicePollStates(t *testing.T) {
	e := newDeviceEnv(t, 0)
	d := e.startDevice("cid", "read:user")
	e.now = e.now.Add(6 * time.Second)
	if out := e.poll("cid", d.DeviceCode); out["error"] != "authorization_pending" {
		t.Fatalf("pending: %v", out)
	}
	if got := e.decide(d.UserCode, "ada", "approve"); got != 200 {
		t.Fatalf("approve: %d", got)
	}
	e.now = e.now.Add(6 * time.Second)
	out := e.poll("cid", d.DeviceCode)
	if out["access_token"] == "" || out["access_token"] == nil || out["token_type"] != "bearer" || out["scope"] != "read:user" {
		t.Fatalf("success: %v", out)
	}
	e.now = e.now.Add(6 * time.Second)
	if out := e.poll("cid", d.DeviceCode); out["error"] != "incorrect_device_code" {
		t.Errorf("second poll after success: %v", out)
	}
}

func TestDevicePollErrors(t *testing.T) {
	e := newDeviceEnv(t, 0)
	d := e.startDevice("cid", "read:user")
	e.now = e.now.Add(6 * time.Second)
	post := func(f url.Values) map[string]any {
		_, body := e.post("/login/oauth/access_token", f, "application/json")
		var out map[string]any
		_ = json.Unmarshal([]byte(body), &out)
		return out
	}
	for _, tc := range []struct {
		name string
		form url.Values
		want string
	}{
		{"wrong grant type", url.Values{"client_id": {"cid"}, "device_code": {d.DeviceCode}, "grant_type": {"password"}}, "unsupported_grant_type"},
		{"unknown device code", url.Values{"client_id": {"cid"}, "device_code": {"zz"}, "grant_type": {grantType}}, "incorrect_device_code"},
		{"another client's code", url.Values{"client_id": {"dev2"}, "device_code": {d.DeviceCode}, "grant_type": {grantType}}, "incorrect_device_code"},
		{"unknown client", url.Values{"client_id": {"nope"}, "device_code": {d.DeviceCode}, "grant_type": {grantType}}, "incorrect_client_credentials"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if out := post(tc.form); out["error"] != tc.want {
				t.Errorf("got %v", out)
			}
		})
	}
}

func TestDeviceSlowDown(t *testing.T) {
	e := newDeviceEnv(t, 0)
	d := e.startDevice("cid", "read:user")
	// The first poll is never early, even straight after the code was issued.
	if out := e.poll("cid", d.DeviceCode); out["error"] != "authorization_pending" {
		t.Fatalf("first poll: %v", out)
	}
	out := e.poll("cid", d.DeviceCode)
	if out["error"] != "slow_down" || out["interval"] != float64(10) || out["error_uri"] == nil || out["error_description"] == nil {
		t.Fatalf("second poll straight after: %v", out)
	}
	// 6s later is past the old interval but inside the bumped one.
	e.now = e.now.Add(6 * time.Second)
	if out := e.poll("cid", d.DeviceCode); out["error"] != "slow_down" || out["interval"] != float64(15) {
		t.Fatalf("still inside the new interval: %v", out)
	}
	e.now = e.now.Add(16 * time.Second)
	if out := e.poll("cid", d.DeviceCode); out["error"] != "authorization_pending" {
		t.Fatalf("after waiting the full interval: %v", out)
	}
	// An approved code is not released to an early poll either.
	e.decide(d.UserCode, "ada", "approve")
	if out := e.poll("cid", d.DeviceCode); out["error"] != "slow_down" {
		t.Fatalf("early poll after approval: %v", out)
	}
}

func TestDeviceDenied(t *testing.T) {
	e := newDeviceEnv(t, 0)
	d := e.startDevice("cid", "read:user")
	if got := e.decide(d.UserCode, "ada", "deny"); got != 200 {
		t.Fatalf("deny: %d", got)
	}
	e.now = e.now.Add(6 * time.Second)
	if out := e.poll("cid", d.DeviceCode); out["error"] != "access_denied" {
		t.Fatalf("got %v", out)
	}
	if got := e.decide(d.UserCode, "ada", "approve"); got != 404 {
		t.Errorf("approving a decided code: %d", got)
	}
}

func TestDeviceExpires(t *testing.T) {
	e := newDeviceEnv(t, 0)
	d := e.startDevice("cid", "read:user")
	e.decide(d.UserCode, "ada", "approve")
	e.now = e.now.Add(15*time.Minute + time.Second)
	if out := e.poll("cid", d.DeviceCode); out["error"] != "expired_token" {
		t.Fatalf("approved but late: %v", out)
	}
	d2 := e.startDevice("cid", "read:user")
	e.now = e.now.Add(15*time.Minute + time.Second)
	if got := e.decide(d2.UserCode, "ada", "approve"); got != 404 {
		t.Errorf("approving an expired code: %d", got)
	}
}

func TestDeviceDecideValidates(t *testing.T) {
	e := newDeviceEnv(t, 0)
	d := e.startDevice("cid", "read:user")
	for _, tc := range []struct {
		name, code, login, decision string
		want                        int
	}{
		{"unknown code", "BBBB-BBBB", "ada", "approve", 404},
		{"unknown login", d.UserCode, "mallory", "approve", 400},
		{"bad decision", d.UserCode, "ada", "maybe", 400},
		{"lowercase and no dash", strings.ToLower(strings.ReplaceAll(d.UserCode, "-", "")), "ada", "approve", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := e.decide(tc.code, tc.login, tc.decision); got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestDevicePageIsServed(t *testing.T) {
	e := newDeviceEnv(t, 0)
	resp, err := http.Get(e.srv.URL + "/login/device?user_code=ABCD-EFGH")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body := readAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(body, `value="ABCD-EFGH"`) || !strings.Contains(body, "ada") {
		t.Errorf("page: %d %s", resp.StatusCode, body)
	}
}

func TestDeviceTokenWorksLikeAWebToken(t *testing.T) {
	e := newDeviceEnv(t, 0)
	tok := e.deviceToken("cid", "ada", "read:user user:email")
	resp, u := e.get("/user", tok)
	if resp.StatusCode != 200 || u["login"] != "ada" || u["two_factor_authentication"] != true {
		t.Fatalf("/user: %d %v", resp.StatusCode, u)
	}
	if resp.Header.Get("X-OAuth-Scopes") != "read:user, user:email" {
		t.Errorf("scopes header %q", resp.Header.Get("X-OAuth-Scopes"))
	}
	if r, _ := e.get("/user/emails", tok); r.StatusCode != 200 {
		t.Errorf("/user/emails: %d", r.StatusCode)
	}
	if got := e.revoke("cid", "sec", "cid", `{"access_token":"`+tok+`"}`); got != 204 {
		t.Fatalf("revoke: %d", got)
	}
	if r, _ := e.get("/user", tok); r.StatusCode != 401 {
		t.Errorf("after revoke: %d", r.StatusCode)
	}
}

func TestDeviceGrantTrimsScopes(t *testing.T) {
	e := newDeviceEnv(t, 0)
	d := e.startDevice("cid", "read:user user:email")
	e.decide(d.UserCode, "bob", "approve", "user:email")
	e.now = e.now.Add(6 * time.Second)
	out := e.poll("cid", d.DeviceCode)
	if out["scope"] != "user:email" {
		t.Fatalf("scope %v", out["scope"])
	}
	if _, u := e.get("/user", out["access_token"].(string)); u["two_factor_authentication"] != nil {
		t.Errorf("2FA field leaked without read:user: %v", u)
	}
}

func TestTokenExpiry(t *testing.T) {
	e := newDeviceEnv(t, time.Hour)
	dev := e.deviceToken("cid", "ada", "read:user")
	web := e.signIn("ada", "read:user")
	_ = web // sign-in goes through the "cid" client's web flow
	for name, tok := range map[string]string{"device": dev, "web": web} {
		if r, _ := e.get("/user", tok); r.StatusCode != 200 {
			t.Fatalf("%s fresh: %d", name, r.StatusCode)
		}
	}
	e.now = e.now.Add(time.Hour + time.Second)
	for name, tok := range map[string]string{"device": dev, "web": web} {
		if r, _ := e.get("/user", tok); r.StatusCode != 401 {
			t.Errorf("%s after ttl: %d", name, r.StatusCode)
		}
		if code, _ := e.postJSON("/applications/cid/token", "cid", "sec", `{"access_token":"`+tok+`"}`); code != 404 {
			t.Errorf("%s check-token after ttl: %d", name, code)
		}
	}
}

func TestCheckToken(t *testing.T) {
	e := newDeviceEnv(t, 0)
	tok := e.deviceToken("cid", "ada", "read:user user:email")
	body := `{"access_token":"` + tok + `"}`
	code, out := e.postJSON("/applications/cid/token", "cid", "sec", body)
	app, _ := out["app"].(map[string]any)
	user, _ := out["user"].(map[string]any)
	scopes, _ := out["scopes"].([]any)
	if code != 200 || app["client_id"] != "cid" || user["login"] != "ada" || user["id"] != float64(1001) || len(scopes) != 2 || out["token"] != tok {
		t.Fatalf("check-token: %d %v", code, out)
	}
	for _, tc := range []struct {
		name, client, secret, path, body string
		want                             int
	}{
		{"wrong secret", "cid", "bad", "cid", body, 401},
		{"unknown client", "nope", "x", "nope", body, 401},
		{"no basic auth", "", "", "cid", body, 401},
		{"path names another client", "cid", "sec", "dev2", body, 404},
		{"another client's credentials", "dev2", "s3", "dev2", body, 404},
		{"unknown token", "cid", "sec", "cid", `{"access_token":"gho_nope"}`, 404},
		{"missing token", "cid", "sec", "cid", `{}`, 422},
		{"not json", "cid", "sec", "cid", `x`, 422},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got int
			if tc.client == "" {
				got, _ = e.post("/applications/"+tc.path+"/token", url.Values{}, "")
			} else {
				got, _ = e.postJSON("/applications/"+tc.path+"/token", tc.client, tc.secret, tc.body)
			}
			if got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
	// Checking does not consume the token; revoking does.
	if r, _ := e.get("/user", tok); r.StatusCode != 200 {
		t.Errorf("token gone after check: %d", r.StatusCode)
	}
	e.revoke("cid", "sec", "cid", body)
	if code, _ := e.postJSON("/applications/cid/token", "cid", "sec", body); code != 404 {
		t.Errorf("after revoke: %d", code)
	}
}

func TestCheckTokenRefusesAnotherClientsToken(t *testing.T) {
	e := newDeviceEnv(t, 0)
	tok := e.deviceToken("dev2", "ada", "read:user")
	body := `{"access_token":"` + tok + `"}`
	if code, _ := e.postJSON("/applications/cid/token", "cid", "sec", body); code != 404 {
		t.Fatalf("cid validated dev2's token: %d", code)
	}
	if code, _ := e.postJSON("/applications/dev2/token", "dev2", "s3", body); code != 200 {
		t.Fatalf("owner check: %d", code)
	}
	// Revoke is refused for the wrong app too, so the token survives.
	if got := e.revoke("cid", "sec", "cid", body); got != 404 {
		t.Errorf("cross-client revoke: %d", got)
	}
	if r, _ := e.get("/user", tok); r.StatusCode != 200 {
		t.Errorf("token died: %d", r.StatusCode)
	}
}

func TestWebFlowStillWorksWithGrantTypeAbsent(t *testing.T) {
	e := newDeviceEnv(t, 0)
	if tok := e.signIn("bob", "read:user"); tok == "" {
		t.Fatal("no token")
	}
}

func TestWebExchangeAcceptsAuthorizationCodeGrantType(t *testing.T) {
	e := newDeviceEnv(t, 0)
	cb := e.authorize(authQuery("bob", "read:user"))
	form := url.Values{"client_id": {"cid"}, "client_secret": {"sec"}, "code": {cb.Get("code")}, "code_verifier": {verifier}, "grant_type": {"authorization_code"}}
	status, body := e.post("/login/oauth/access_token", form, "application/json")
	if status != 200 || !strings.Contains(body, "access_token") {
		t.Fatalf("got %d %s", status, body)
	}
}

func TestExpiredDeviceGrantsAreSwept(t *testing.T) {
	e := newDeviceEnv(t, 0)
	old := e.startDevice("cid", "")
	e.now = e.now.Add(15*time.Minute + time.Second)
	if out := e.poll("cid", old.DeviceCode); out["error"] != "expired_token" {
		t.Fatalf("just expired: %v", out)
	}
	e.now = e.now.Add(2 * time.Hour)
	e.startDevice("cid", "") // sweeps
	if out := e.poll("cid", old.DeviceCode); out["error"] != "incorrect_device_code" {
		t.Fatalf("long expired: %v", out)
	}
}
