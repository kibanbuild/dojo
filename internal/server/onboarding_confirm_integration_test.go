//go:build integration

package server_test

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kibanyadev/dojo/internal/server"
)

func signedInAda(t *testing.T) *browser {
	t.Helper()
	resetLearners(t, "1001")
	ada := newBrowser(t)
	if resp := ada.signIn("ada", "", "age=on&terms=on", false); resp.Header.Get("Location") != "/app" {
		t.Fatalf("ada sign-in: %q", resp.Header.Get("Location"))
	}
	return ada
}

func TestPatchMeHostedDefault(t *testing.T) {
	ada := signedInAda(t)
	if _, me := ada.me(); me["hosted_default"] != false {
		t.Fatalf("new learner: %v", me["hosted_default"])
	}
	before := asOwner(t, "SELECT updated_at FROM learners WHERE github_id = 1001")
	code, body := ada.patch(`{"hosted_default":true}`, true)
	if code != http.StatusOK || !strings.Contains(body, `"hosted_default":true`) {
		t.Fatalf("set: %d %s", code, body)
	}
	if got := asOwner(t, "SELECT hosted_default FROM learners WHERE github_id = 1001"); got != "t" {
		t.Fatalf("stored: %q", got)
	}
	if after := asOwner(t, "SELECT updated_at FROM learners WHERE github_id = 1001"); after == before {
		t.Fatal("updated_at did not move")
	}
	if _, me := ada.me(); me["hosted_default"] != true {
		t.Fatalf("GET /v1/me: %v", me["hosted_default"])
	}
	for _, body := range []string{`{"hosted_default":"yes"}`, `{"hosted_default":1}`, `{"hosted_default":null}`, `{"hosted_default":[true]}`} {
		if code, out := ada.patch(body, true); code != http.StatusBadRequest || !strings.Contains(out, "bad_request") {
			t.Fatalf("%s: %d %s", body, code, out)
		}
	}
	if code, _ := ada.patch(`{"hosted_default":false}`, false); code != http.StatusForbidden {
		t.Fatalf("without CSRF: %d", code)
	}
	if got := asOwner(t, "SELECT hosted_default FROM learners WHERE github_id = 1001"); got != "t" {
		t.Fatalf("a refusal changed it: %q", got)
	}
	if code, body := ada.patch(`{"hosted_default":false,"language":"es"}`, true); code != http.StatusOK || !strings.Contains(body, `"hosted_default":false`) {
		t.Fatalf("unset: %d %s", code, body)
	}
}

func TestOnboardingHostedDefaultSkipsO3(t *testing.T) {
	freshCatalog(t)
	seedCatalog(t)
	ada := signedInAda(t)
	asOwner(t, "UPDATE learners SET chosen_path_id = (SELECT id FROM paths WHERE slug = 'systems-devops') WHERE github_id = 1001")
	cliToken(t, newCLIClient(t), "ada")
	step := func() onboardingBody {
		code, body := ada.getJSON("/v1/onboarding")
		return readOnboarding(t, code, body)
	}
	if o := step(); o.NextStep != "o3" || o.HostedDefault {
		t.Fatalf("default off: %+v", o)
	}
	if code, _ := ada.patch(`{"hosted_default":true}`, true); code != http.StatusOK {
		t.Fatal("patch")
	}
	if o := step(); o.NextStep != "o4" || !o.HostedDefault {
		t.Fatalf("default on: %+v", o)
	}
	if code, _ := ada.patch(`{"hosted_default":false}`, true); code != http.StatusOK {
		t.Fatal("patch")
	}
	if o := step(); o.NextStep != "o3" || o.HostedDefault {
		t.Fatalf("default off again: %+v", o)
	}
}

// bearerSend is a JSON write with the CLI token and the CSRF header the onboarding writes ask of every caller.
func bearerSend(t *testing.T, method, path, tok string) int {
	t.Helper()
	req, _ := http.NewRequest(method, apiURL(t)+path, bytes.NewReader(nil))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(server.CSRFHeader, "1")
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := newCLIClient(t).b.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

const confirmRows = "SELECT check_id, status, text, source FROM doctor_results r JOIN learners l ON l.id = r.learner_id WHERE l.github_id = 1001 ORDER BY check_id"

func TestConfirmationsWriteStreamAndRemove(t *testing.T) {
	ada := signedInAda(t)
	s := openStream(t, apiURL(t), cookieOf(t, ada))
	s.next("ready", 5*time.Second)

	ada.mustSend(http.MethodPut, "/v1/onboarding/confirmations/two_factor", "")
	if ev := s.next("doctor", 5*time.Second); ev.data != `{"checks":[{"id":"two_factor","status":"ok"}]}` {
		t.Fatalf("put frame: %s", ev.data)
	}
	ada.mustSend(http.MethodPut, "/v1/onboarding/confirmations/two_factor", "") // idempotent
	s.next("doctor", 5*time.Second)
	ada.mustSend(http.MethodPut, "/v1/onboarding/confirmations/secret_scanning", "")
	if ev := s.next("doctor", 5*time.Second); ev.data != `{"checks":[{"id":"secret_scanning","status":"ok"}]}` {
		t.Fatalf("second step frame: %s", ev.data)
	}
	if got := asOwner(t, confirmRows); got != "secret_scanning|ok|Confirmed by you|learner\ntwo_factor|ok|Confirmed by you|learner" {
		t.Fatalf("rows: %q", got)
	}
	if got := asOwner(t, "SELECT count(*) FROM doctor_results r JOIN learners l ON l.id = r.learner_id WHERE l.github_id = 1001 AND r.org_id = l.org_id"); got != "2" {
		t.Fatalf("org: %q", got)
	}

	code, body := ada.getJSON("/v1/onboarding")
	o := readOnboarding(t, code, body)
	if len(o.Checks) != 2 || o.Checks[1].ID != "two_factor" || o.Checks[1].Group != "advisory" || o.Checks[1].Source != "learner" || o.Checks[1].Status != "ok" {
		t.Fatalf("checks: %+v", o.Checks)
	}
	if o.NextStep == "done" || o.NextStep == "o4" || o.NextStep == "o5" {
		t.Fatalf("an advisory step moved the flow: %q", o.NextStep)
	}

	ada.mustSend(http.MethodDelete, "/v1/onboarding/confirmations/two_factor", "")
	if ev := s.next("doctor", 5*time.Second); ev.data != `{"checks":[{"id":"two_factor","status":"unknown"}]}` {
		t.Fatalf("delete frame: %s", ev.data)
	}
	ada.mustSend(http.MethodDelete, "/v1/onboarding/confirmations/two_factor", "") // already gone
	if got := asOwner(t, confirmRows); got != "secret_scanning|ok|Confirmed by you|learner" {
		t.Fatalf("after delete: %q", got)
	}

	tok := cliToken(t, newCLIClient(t), "ada")
	if code := bearerSend(t, http.MethodPut, "/v1/onboarding/confirmations/two_factor", tok); code != http.StatusNoContent {
		t.Fatalf("bearer put: %d", code)
	}
	if code := bearerSend(t, http.MethodDelete, "/v1/onboarding/confirmations/two_factor", tok); code != http.StatusNoContent {
		t.Fatalf("bearer delete: %d", code)
	}
}

func TestConfirmationsRefuseWhatTheyShould(t *testing.T) {
	ada := signedInAda(t)
	if code, _ := newBrowser(t).send(http.MethodPut, "/v1/onboarding/confirmations/two_factor", "", true); code != http.StatusUnauthorized {
		t.Fatalf("signed out: %d", code)
	}
	if code, out := ada.send(http.MethodPut, "/v1/onboarding/confirmations/two_factor", "", false); code != http.StatusForbidden || !strings.Contains(out, "csrf_rejected") {
		t.Fatalf("without CSRF: %d %s", code, out)
	}
	for _, c := range [][2]string{{http.MethodPut, "memory"}, {http.MethodDelete, "memory"}, {http.MethodPut, "TWO_FACTOR"}, {http.MethodDelete, "nope"}} {
		if code, out := ada.send(c[0], "/v1/onboarding/confirmations/"+c[1], "", true); code != http.StatusNotFound || !strings.Contains(out, "not_found") {
			t.Fatalf("%s %s: %d %s", c[0], c[1], code, out)
		}
	}
	if got := asOwner(t, confirmRows); got != "" {
		t.Fatalf("a refusal wrote rows: %q", got)
	}
}

func TestConfirmationsShareTheWriteBudget(t *testing.T) {
	signedInAda(t)
	tok := cliToken(t, newCLIClient(t), "ada")
	var limited int
	for i := 0; i < 15; i++ {
		switch code := bearerSend(t, http.MethodPut, "/v1/onboarding/confirmations/two_factor", tok); code {
		case http.StatusTooManyRequests:
			limited++
		case http.StatusNoContent:
		default:
			t.Fatalf("call %d: %d", i, code)
		}
	}
	if limited != 5 {
		t.Fatalf("%d of 15 calls limited, want 5 past the burst of 10", limited)
	}
}
