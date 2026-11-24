package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kibanbuild/dojo/internal/server/repo"
)

type fakeBundles struct {
	rows    map[string]repo.GetTicketBundleRow
	err     error
	signErr error
	signed  []string
	ttl     time.Duration
}

func (f *fakeBundles) GetTicketBundle(_ context.Context, id string) (repo.GetTicketBundleRow, error) {
	if f.err != nil {
		return repo.GetTicketBundleRow{}, f.err
	}
	r, ok := f.rows[id]
	if !ok {
		return repo.GetTicketBundleRow{}, pgx.ErrNoRows
	}
	return r, nil
}

func (f *fakeBundles) PresignGet(_ context.Context, key string, ttl time.Duration) (string, error) {
	f.signed, f.ttl = append(f.signed, key), ttl
	if f.signErr != nil {
		return "", f.signErr
	}
	return "http://localhost:9000/dojo-catalog/" + key + "?sig=x", nil
}

func bundleFake() *fakeBundles {
	return &fakeBundles{rows: map[string]repo.GetTicketBundleRow{
		"support-l1/perms-03": {ID: "support-l1/perms-03", CatalogVersion: "a1b2c3d4e5f6",
			BundleKey:    pgtype.Text{String: "bundles/support-l1/perms-03/" + strings.Repeat("a", 64) + ".tar.gz", Valid: true},
			BundleSha256: pgtype.Text{String: strings.Repeat("a", 64), Valid: true}, BundleBytes: pgtype.Int4{Int32: 1234, Valid: true}},
	}}
}

func getBundle(t *testing.T, f *fakeBundles, path string, suspend, anon bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if !anon {
		req.AddCookie(&http.Cookie{Name: SessionCookie, Value: goodToken})
	}
	rec := httptest.NewRecorder()
	auth := Auth{Store: &fakeSessions{row: activeRow(time.Now(), suspend)}, Bundles: &Bundles{Store: f, Signer: f}}
	New(fakeReady{}, auth, NewRateLimits(1), nil, nil).ServeHTTP(rec, req)
	return rec
}

func TestBundleFetch(t *testing.T) {
	f := bundleFake()
	before := time.Now()
	rec := getBundle(t, f, "/v1/tickets/support-l1/perms-03/bundle", false, false)
	raw := map[string]any{}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil || rec.Code != 200 {
		t.Fatalf("%d %v %s", rec.Code, err, rec.Body)
	}
	if len(raw) != 6 {
		t.Fatalf("fields: %v", raw)
	}
	if raw["item_id"] != "support-l1/perms-03" || raw["catalog_version"] != "a1b2c3d4e5f6" || raw["sha256"] != strings.Repeat("a", 64) ||
		raw["bytes"] != float64(1234) || !strings.Contains(raw["url"].(string), "bundles/support-l1/perms-03/") {
		t.Fatalf("body: %v", raw)
	}
	exp, err := time.Parse(time.RFC3339, raw["expires_at"].(string))
	if err != nil || exp.Before(before.Add(bundleTTL-time.Second)) || exp.After(time.Now().Add(bundleTTL+time.Second)) {
		t.Fatalf("expires_at %v %v", exp, err)
	}
	if f.ttl != bundleTTL || bundleTTL != 10*time.Minute {
		t.Fatalf("ttl %v", f.ttl)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("cache: %q", rec.Header().Get("Cache-Control"))
	}
}

func TestBundleErrors(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		suspend bool
		anon    bool
		f       func() *fakeBundles
		code    int
		body    string
	}{
		{"anonymous", "/v1/tickets/support-l1/perms-03/bundle", false, true, bundleFake, 401, "unauthenticated"},
		{"suspended", "/v1/tickets/support-l1/perms-03/bundle", true, false, bundleFake, 403, "account_suspended"},
		{"unknown id", "/v1/tickets/support-l1/nope/bundle", false, false, bundleFake, 404, "not_found"},
		{"bad shape", "/v1/tickets/Support_L1/perms-03/bundle", false, false, bundleFake, 404, "not_found"},
		{"one segment", "/v1/tickets/perms-03/bundle", false, false, bundleFake, 404, "not_found"},
		{"three segments", "/v1/tickets/a/b/c/bundle", false, false, bundleFake, 404, "not_found"},
		{"no suffix", "/v1/tickets/support-l1/perms-03", false, false, bundleFake, 404, "not_found"},
		{"other suffix", "/v1/tickets/support-l1/perms-03/progress", false, false, bundleFake, 404, "not_found"},
		{"database", "/v1/tickets/support-l1/perms-03/bundle", false, false,
			func() *fakeBundles { f := bundleFake(); f.err = errors.New("down"); return f }, 500, "server_error"},
		{"presign", "/v1/tickets/support-l1/perms-03/bundle", false, false,
			func() *fakeBundles { f := bundleFake(); f.signErr = errors.New("kms"); return f }, 500, "server_error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := tt.f()
			rec := getBundle(t, f, tt.path, tt.suspend, tt.anon)
			if rec.Code != tt.code || !strings.Contains(rec.Body.String(), `"`+tt.body+`"`) {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			if tt.code != 500 && len(f.signed) != 0 {
				t.Fatalf("signed on %s", tt.name)
			}
		})
	}
}

func TestBundleRateLimitIsItsOwnBudget(t *testing.T) {
	f := bundleFake()
	auth := Auth{Store: &fakeSessions{row: activeRow(time.Now(), false)}, Bundles: &Bundles{Store: f, Signer: f}, Catalog: populatedCatalog()}
	rl := NewRateLimits(1)
	h := New(fakeReady{}, auth, rl, nil, nil)
	call := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(&http.Cookie{Name: SessionCookie, Value: goodToken})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	for i := 0; i < bundleBurst; i++ {
		if rec := call("/v1/tickets/support-l1/perms-03/bundle"); rec.Code != 200 {
			t.Fatalf("request %d: %d", i, rec.Code)
		}
	}
	rec := call("/v1/tickets/support-l1/perms-03/bundle")
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" || !strings.Contains(rec.Body.String(), `"rate_limited"`) {
		t.Fatalf("31st: %d %q %s", rec.Code, rec.Header().Get("Retry-After"), rec.Body)
	}
	if rec := call("/v1/catalog"); rec.Code != 200 {
		t.Fatalf("catalog shares the bundle budget: %d", rec.Code)
	}
}

func TestBundleRouteAbsentWithoutStore(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/tickets/support-l1/perms-03/bundle", nil)
	New(fakeReady{}, Auth{Store: &fakeSessions{}}, NewRateLimits(1), nil, nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("%d", rec.Code)
	}
}
