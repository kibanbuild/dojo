package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kibanbuild/dojo/internal/server/repo"
)

// fakeDevice is a cli_tokens row: who owns it and whether it is revoked.
type fakeDevice struct {
	row     repo.ListLearnerCLITokensRow
	learner pgtype.UUID
	revoked bool
}

func (f *fakeSessions) ListLearnerCLITokens(_ context.Context, learner pgtype.UUID) ([]repo.ListLearnerCLITokensRow, error) {
	if f.devErr != nil {
		return nil, f.devErr
	}
	var out []repo.ListLearnerCLITokensRow
	for _, d := range f.devs {
		if d.learner == learner && !d.revoked {
			out = append(out, d.row)
		}
	}
	// The query orders by last_used_at, newest first.
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastUsedAt.Time.After(out[j].LastUsedAt.Time) })
	return out, nil
}

// RevokeLearnerCLIToken mirrors the SQL: id, owner and not-yet-revoked must all match.
func (f *fakeSessions) RevokeLearnerCLIToken(_ context.Context, a repo.RevokeLearnerCLITokenParams) (int64, error) {
	if f.devErr != nil {
		return 0, f.devErr
	}
	for _, d := range f.devs {
		if d.row.ID == a.ID && d.learner == a.LearnerID && !d.revoked {
			d.revoked = true
			if f.cli != nil && a.ID == f.cli.ID {
				f.cli = nil
			}
			return 1, nil
		}
	}
	return 0, nil
}

var (
	adaID = pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	bobID = pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
)

func devUUID(n byte) pgtype.UUID { return pgtype.UUID{Bytes: [16]byte{15: n}, Valid: true} }

func newDeviceFake() *fakeSessions {
	now := time.Now().UTC().Truncate(time.Second)
	dev := func(id pgtype.UUID, owner pgtype.UUID, name string, used time.Duration) *fakeDevice {
		return &fakeDevice{learner: owner, row: repo.ListLearnerCLITokensRow{
			ID: id, DeviceName: name, Os: "linux/amd64", CliVersion: "v1.2.3",
			CreatedAt:  pgtype.Timestamptz{Time: now.Add(-48 * time.Hour), Valid: true},
			LastUsedAt: pgtype.Timestamptz{Time: now.Add(-used), Valid: true},
		}}
	}
	f := &fakeSessions{cli: activeCLI(now, false), row: activeRow(now, false)}
	f.cli.ID = devUUID(7)
	f.devs = []*fakeDevice{
		dev(devUUID(5), adaID, "old laptop", time.Hour),
		dev(devUUID(7), adaID, "this laptop", 0),
		dev(devUUID(9), bobID, "bob laptop", 0),
	}
	return f
}

type devCall struct {
	method, path string
	cookie, csrf bool
	anon         bool
	suspend      bool
}

func doDevices(f *fakeSessions, c devCall) *httptest.ResponseRecorder {
	req := httptest.NewRequest(c.method, c.path, nil)
	switch {
	case c.anon:
	case c.cookie:
		f.row = activeRow(time.Now(), c.suspend)
		req.AddCookie(&http.Cookie{Name: SessionCookie, Value: goodToken})
		if c.csrf {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set(CSRFHeader, "1")
		}
	default:
		f.cli = activeCLI(time.Now(), c.suspend)
		f.cli.ID = devUUID(7)
		req.Header.Set("Authorization", "Bearer "+goodCLIToken)
	}
	rec := httptest.NewRecorder()
	New(fakeReady{}, Auth{Store: f}, NewRateLimits(1), nil, nil).ServeHTTP(rec, req)
	return rec
}

func TestListDevices(t *testing.T) {
	type dev struct {
		ID         string `json:"id"`
		DeviceName string `json:"device_name"`
		OS         string `json:"os"`
		CLIVersion string `json:"cli_version"`
		Current    bool   `json:"current"`
	}
	for _, tt := range []struct {
		name   string
		call   devCall
		wantID []string
		cur    string
	}{
		{"bearer marks its own token, newest last_used first", devCall{method: "GET", path: "/v1/me/devices"}, []string{devUUID(7).String(), devUUID(5).String()}, devUUID(7).String()},
		{"cookie marks none", devCall{method: "GET", path: "/v1/me/devices", cookie: true}, []string{devUUID(7).String(), devUUID(5).String()}, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newDeviceFake()
			rec := doDevices(f, tt.call)
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d %s", rec.Code, rec.Body)
			}
			var got struct{ Devices []dev }
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got.Devices) != len(tt.wantID) {
				t.Fatalf("body %v %s", err, rec.Body)
			}
			for i, d := range got.Devices {
				if d.ID != tt.wantID[i] || d.Current != (d.ID == tt.cur) {
					t.Fatalf("device %d: %+v", i, d)
				}
			}
			if got.Devices[1].DeviceName != "old laptop" || got.Devices[1].OS != "linux/amd64" || got.Devices[1].CLIVersion != "v1.2.3" {
				t.Fatalf("fields: %+v", got.Devices[1])
			}
			var raw struct{ Devices []map[string]any }
			_ = json.Unmarshal(rec.Body.Bytes(), &raw)
			for _, k := range []string{"created_at", "last_used_at", "current"} {
				if _, ok := raw.Devices[0][k]; !ok {
					t.Fatalf("missing %s: %s", k, rec.Body)
				}
			}
			if strings.Contains(strings.ToLower(rec.Body.String()), "hash") || len(raw.Devices[0]) != 7 {
				t.Fatalf("unexpected keys: %s", rec.Body)
			}
		})
	}
}

func TestListDevicesEmptyIsAnArray(t *testing.T) {
	f := newDeviceFake()
	f.devs = nil
	if rec := doDevices(f, devCall{method: "GET", path: "/v1/me/devices"}); rec.Body.String() != "{\"devices\":[]}\n" {
		t.Fatalf("%d %q", rec.Code, rec.Body)
	}
}

func TestDevicesAuthentication(t *testing.T) {
	if rec := doDevices(newDeviceFake(), devCall{method: "GET", path: "/v1/me/devices", anon: true}); rec.Code != 401 {
		t.Fatalf("list anonymous: %d", rec.Code)
	}
	if rec := doDevices(newDeviceFake(), devCall{method: "DELETE", path: "/v1/me/devices/" + devUUID(5).String(), anon: true}); rec.Code != 401 {
		t.Fatalf("revoke anonymous: %d", rec.Code)
	}
}

// A suspended learner cannot list, but can still end their own devices (self-service, like sign-out).
func TestDevicesSuspended(t *testing.T) {
	for _, cookie := range []bool{false, true} {
		f := newDeviceFake()
		rec := doDevices(f, devCall{method: "GET", path: "/v1/me/devices", cookie: cookie, csrf: true, suspend: true})
		if rec.Code != 403 || rec.Body.String() != "{\"error\":\"account_suspended\"}\n" {
			t.Fatalf("list suspended (cookie=%v): %d %s", cookie, rec.Code, rec.Body)
		}
		rec = doDevices(f, devCall{method: "DELETE", path: "/v1/me/devices/" + devUUID(5).String(), cookie: cookie, csrf: true, suspend: true})
		if rec.Code != 204 || !f.devs[0].revoked {
			t.Fatalf("revoke own suspended (cookie=%v): %d %s", cookie, rec.Code, rec.Body)
		}
		rec = doDevices(f, devCall{method: "DELETE", path: "/v1/me/devices/" + devUUID(9).String(), cookie: cookie, csrf: true, suspend: true})
		if rec.Code != 404 || f.devs[2].revoked {
			t.Fatalf("revoke foreign suspended (cookie=%v): %d", cookie, rec.Code)
		}
	}
	if rec := doDevices(newDeviceFake(), devCall{method: "DELETE", path: "/v1/me/devices/" + devUUID(5).String(), cookie: true, suspend: true}); rec.Code != 403 || !strings.Contains(rec.Body.String(), "csrf_rejected") {
		t.Fatalf("suspended cookie without CSRF: %d %s", rec.Code, rec.Body)
	}
}

func TestRevokeDevice(t *testing.T) {
	notFound := "{\"error\":\"not_found\"}\n"
	for _, tt := range []struct {
		name    string
		id      string
		call    devCall
		want    int
		body    string
		revoked int // index into devs expected revoked, -1 none
	}{
		{"own other device by bearer", devUUID(5).String(), devCall{}, 204, "", 0},
		{"own other device by cookie", devUUID(5).String(), devCall{cookie: true, csrf: true}, 204, "", 0},
		{"own current token is a logout", devUUID(7).String(), devCall{}, 204, "", 1},
		{"foreign", devUUID(9).String(), devCall{}, 404, notFound, -1},
		{"unknown", devUUID(99).String(), devCall{}, 404, notFound, -1},
		{"malformed", "not-a-uuid", devCall{}, 404, notFound, -1},
		{"uuid without hyphens", strings.ReplaceAll(devUUID(5).String(), "-", ""), devCall{}, 404, notFound, -1},
		{"underscores", strings.ReplaceAll(devUUID(5).String(), "-", "_"), devCall{}, 404, notFound, -1},
		{"dashes in the wrong places", "0000000-00000-0000-0000-000000000005", devCall{}, 404, notFound, -1},
		{"braces", "{" + devUUID(5).String()[:35], devCall{}, 404, notFound, -1},
		{"non-hex", "0000000g-0000-0000-0000-000000000005", devCall{}, 404, notFound, -1},
		{"uppercase hex is accepted", strings.ToUpper(devUUID(5).String()), devCall{}, 204, "", 0},
		{"cookie without CSRF", devUUID(5).String(), devCall{cookie: true}, 403, "{\"error\":\"csrf_rejected\"}\n", -1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newDeviceFake()
			tt.call.method, tt.call.path = "DELETE", "/v1/me/devices/"+tt.id
			rec := doDevices(f, tt.call)
			if rec.Code != tt.want || rec.Body.String() != tt.body {
				t.Fatalf("%d %q", rec.Code, rec.Body)
			}
			for i, d := range f.devs {
				if d.revoked != (i == tt.revoked) {
					t.Fatalf("device %d revoked=%v", i, d.revoked)
				}
			}
		})
	}
}

func TestRevokeDeviceTwiceAndOwnTokenStopsWorking(t *testing.T) {
	f := newDeviceFake()
	own := "/v1/me/devices/" + devUUID(7).String()
	if rec := doDevices(f, devCall{method: "DELETE", path: own}); rec.Code != 204 {
		t.Fatalf("first: %d", rec.Code)
	}
	// The fake emptied f.cli, as the database stops resolving a revoked token.
	req := httptest.NewRequest("GET", "/v1/me/devices", nil)
	req.Header.Set("Authorization", "Bearer "+goodCLIToken)
	rec := httptest.NewRecorder()
	New(fakeReady{}, Auth{Store: f}, NewRateLimits(1), nil, nil).ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("after revoking itself: %d", rec.Code)
	}
	if rec := doDevices(f, devCall{method: "DELETE", path: "/v1/me/devices/" + devUUID(5).String(), cookie: true, csrf: true}); rec.Code != 204 {
		t.Fatalf("other: %d", rec.Code)
	}
	if rec := doDevices(f, devCall{method: "DELETE", path: "/v1/me/devices/" + devUUID(5).String(), cookie: true, csrf: true}); rec.Code != 404 {
		t.Fatalf("already revoked: %d", rec.Code)
	}
}

func TestDevicesDatabaseFailure(t *testing.T) {
	for _, m := range []struct{ method, path string }{{"GET", "/v1/me/devices"}, {"DELETE", "/v1/me/devices/" + devUUID(5).String()}} {
		f := newDeviceFake()
		f.devErr = context.DeadlineExceeded
		if rec := doDevices(f, devCall{method: m.method, path: m.path}); rec.Code != 500 || rec.Body.String() != "{\"error\":\"server_error\"}\n" {
			t.Fatalf("%s: %d %s", m.method, rec.Code, rec.Body)
		}
	}
}
