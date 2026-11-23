//go:build integration

package server_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/kibanbuild/dojo/internal/server"
)

// del sends DELETE as the browser: its cookie, plus the CSRF headers when csrf is set.
func (b *browser) del(path string, csrf bool) (int, string) {
	b.t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, apiURL(b.t)+path, nil)
	if csrf {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(server.CSRFHeader, "1")
	}
	resp, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

type deviceJSON struct {
	ID         string `json:"id"`
	DeviceName string `json:"device_name"`
	OS         string `json:"os"`
	CLIVersion string `json:"cli_version"`
	CreatedAt  string `json:"created_at"`
	LastUsedAt string `json:"last_used_at"`
	Current    bool   `json:"current"`
}

func listDevices(t *testing.T, c *cliClient, tok string) []deviceJSON {
	t.Helper()
	code, body := c.call(http.MethodGet, "/v1/me/devices", tok, nil)
	var out struct{ Devices []deviceJSON }
	if code != http.StatusOK || json.Unmarshal([]byte(body), &out) != nil {
		t.Fatalf("list devices: %d %q", code, body)
	}
	if strings.Contains(strings.ToLower(body), "hash") {
		t.Fatalf("the list mentions a hash: %s", body)
	}
	return out.Devices
}

func currentOf(t *testing.T, ds []deviceJSON) string {
	t.Helper()
	cur := ""
	for _, d := range ds {
		if d.Current {
			if cur != "" {
				t.Fatalf("two current devices: %+v", ds)
			}
			cur = d.ID
		}
	}
	return cur
}

func TestDevicesListAndRevoke(t *testing.T) {
	resetLearners(t, "1001", "1002")
	signUp(t, "ada")
	signUp(t, "bob")
	adaWeb := newBrowser(t)
	if resp := adaWeb.signIn("ada", "", "age=on&terms=on", false); resp.Header.Get("Location") != "/app" {
		t.Fatalf("ada sign-in: %q", resp.Header.Get("Location"))
	}
	adaCLI, bobCLI := newCLIClient(t), newCLIClient(t)
	a1, a2, b1 := cliToken(t, adaCLI, "ada"), cliToken(t, adaCLI, "ada"), cliToken(t, bobCLI, "bob")

	// Ada sees her two devices, never bob's, and exactly the calling token is current.
	ds1 := listDevices(t, adaCLI, a1)
	ds2 := listDevices(t, adaCLI, a2)
	if len(ds1) != 2 || len(ds2) != 2 {
		t.Fatalf("ada devices: %+v / %+v", ds1, ds2)
	}
	id1, id2 := currentOf(t, ds1), currentOf(t, ds2)
	if id1 == "" || id2 == "" || id1 == id2 {
		t.Fatalf("current flags: %q %q", id1, id2)
	}
	if d := ds1[0]; d.DeviceName != "integration laptop" || d.OS != "linux/amd64" || d.CLIVersion != "v0.0.0-test" || d.CreatedAt == "" || d.LastUsedAt == "" {
		t.Fatalf("fields: %+v", d)
	}
	// The id is the row id, not the token hash.
	if got := asOwner(t, "SELECT count(*) FROM cli_tokens WHERE id = '"+id1+"' AND learner_id = (SELECT id FROM learners WHERE github_id = 1001)"); got != "1" {
		t.Fatalf("id is not ada's row id: %s", got)
	}
	// A cookie call lists the same devices with none current.
	code, body := adaWeb.getJSON("/v1/me/devices")
	if code != http.StatusOK || strings.Contains(body, `"current":true`) || strings.Count(body, `"id"`) != 2 {
		t.Fatalf("cookie list: %d %q", code, body)
	}
	bobDevices := listDevices(t, bobCLI, b1)
	if len(bobDevices) != 1 || !bobDevices[0].Current {
		t.Fatalf("bob devices: %+v", bobDevices)
	}
	bobID := bobDevices[0].ID

	notFound := "{\"error\":\"not_found\"}\n"
	// Bob's token is not Ada's to revoke: 404 by bearer and by cookie, and it keeps working.
	if code, body := adaCLI.call(http.MethodDelete, "/v1/me/devices/"+bobID, a1, nil); code != http.StatusNotFound || body != notFound {
		t.Fatalf("foreign revoke: %d %q", code, body)
	}
	if code, body := adaWeb.del("/v1/me/devices/"+bobID, true); code != http.StatusNotFound || body != notFound {
		t.Fatalf("foreign revoke by cookie: %d %q", code, body)
	}
	if code, _ := bobCLI.call(http.MethodGet, "/v1/me", b1, nil); code != http.StatusOK {
		t.Fatalf("bob's token after a foreign revoke: %d", code)
	}
	if got := asOwner(t, "SELECT count(*) FROM cli_tokens WHERE id = '"+bobID+"' AND revoked_at IS NULL"); got != "1" {
		t.Fatalf("bob's row was revoked: %s", got)
	}
	// Unknown and malformed ids answer the same.
	for _, id := range []string{"01900000-0000-7000-8000-000000000000", "nope", "1", strings.ReplaceAll(id2, "-", "")} {
		if code, body := adaCLI.call(http.MethodDelete, "/v1/me/devices/"+id, a1, nil); code != http.StatusNotFound || body != notFound {
			t.Fatalf("id %q: %d %q", id, code, body)
		}
	}
	if code, _ := adaCLI.call(http.MethodDelete, "/v1/me/devices/"+id2, "", nil); code != http.StatusUnauthorized {
		t.Fatalf("anonymous revoke: %d", code)
	}

	// The cookie needs CSRF; refused, nothing changes; then Ada revokes her other device from the web.
	if code, _ := adaWeb.del("/v1/me/devices/"+id2, false); code != http.StatusForbidden {
		t.Fatalf("without CSRF: %d", code)
	}
	if code, _ := adaCLI.call(http.MethodGet, "/v1/me", a2, nil); code != http.StatusOK {
		t.Fatalf("a2 after a refused revoke: %d", code)
	}
	if code, body := adaWeb.del("/v1/me/devices/"+id2, true); code != http.StatusNoContent || body != "" {
		t.Fatalf("revoke other: %d %q", code, body)
	}
	if code, _ := adaCLI.call(http.MethodGet, "/v1/me", a2, nil); code != http.StatusUnauthorized {
		t.Fatalf("a2 after revoke: %d", code)
	}
	if code, body := adaWeb.del("/v1/me/devices/"+id2, true); code != http.StatusNotFound || body != notFound {
		t.Fatalf("revoke twice: %d %q", code, body)
	}
	if ds := listDevices(t, adaCLI, a1); len(ds) != 1 || ds[0].ID != id1 {
		t.Fatalf("list after revoke: %+v", ds)
	}

	// Revoking the token the call carries is a sign-out: 204, then 401.
	if code, body := adaCLI.call(http.MethodDelete, "/v1/me/devices/"+id1, a1, nil); code != http.StatusNoContent || body != "" {
		t.Fatalf("revoke current: %d %q", code, body)
	}
	if code, _ := adaCLI.call(http.MethodGet, "/v1/me/devices", a1, nil); code != http.StatusUnauthorized {
		t.Fatalf("a1 after revoking itself: %d", code)
	}
	if got := asOwner(t, "SELECT count(*) FROM cli_tokens WHERE learner_id = (SELECT id FROM learners WHERE github_id = 1001) AND revoked_at IS NOT NULL"); got != "2" {
		t.Fatalf("ada revoked rows: %s", got)
	}
	if ds := listDevices(t, bobCLI, b1); len(ds) != 1 {
		t.Fatalf("bob after all of it: %+v", ds)
	}
	// The cookie session is untouched by revoking CLI tokens, and the list is now empty.
	if code, body := adaWeb.getJSON("/v1/me/devices"); code != http.StatusOK || body != "{\"devices\":[]}\n" {
		t.Fatalf("empty list: %d %q", code, body)
	}
}

func TestDevicesSuspendedLearner(t *testing.T) {
	resetLearners(t, "1001", "1002")
	signUp(t, "ada")
	c := newCLIClient(t)
	tok := cliToken(t, c, "ada")
	id := currentOf(t, listDevices(t, c, tok))
	asOwner(t, "UPDATE learners SET suspended_at = now() WHERE github_id = 1001")
	forbidden := "{\"error\":\"account_suspended\"}\n"
	if code, body := c.call(http.MethodGet, "/v1/me/devices", tok, nil); code != http.StatusForbidden || body != forbidden {
		t.Fatalf("suspended list: %d %q", code, body)
	}
	// Ending their own device stays allowed; another learner's id is still 404.
	signUp(t, "bob")
	bobCLI := newCLIClient(t)
	bobTok := cliToken(t, bobCLI, "bob")
	bobID := currentOf(t, listDevices(t, bobCLI, bobTok))
	if code, body := c.call(http.MethodDelete, "/v1/me/devices/"+bobID, tok, nil); code != http.StatusNotFound || body != "{\"error\":\"not_found\"}\n" {
		t.Fatalf("suspended foreign revoke: %d %q", code, body)
	}
	if code, body := c.call(http.MethodDelete, "/v1/me/devices/"+id, tok, nil); code != http.StatusNoContent || body != "" {
		t.Fatalf("suspended revoke own: %d %q", code, body)
	}
	if code, _ := c.call(http.MethodGet, "/v1/me", tok, nil); code != http.StatusUnauthorized {
		t.Fatalf("token after revoke: %d", code)
	}
	if code, _ := bobCLI.call(http.MethodGet, "/v1/me", bobTok, nil); code != http.StatusOK {
		t.Fatalf("bob after a foreign revoke: %d", code)
	}
}

// getJSON sends a GET with the browser's cookie and returns status and body.
func (b *browser) getJSON(path string) (int, string) {
	b.t.Helper()
	resp, err := b.c.Get(apiURL(b.t) + path)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}
