package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const wantCSP = "default-src 'none'; style-src 'self'; base-uri 'none'; frame-ancestors 'none'"

func get(t *testing.T, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Host = Host
	rec := httptest.NewRecorder()
	New(fakeReady{}, Deps{}).ServeHTTP(rec, req)
	return rec
}

func TestStylesheetRoute(t *testing.T) {
	tests := []struct {
		name, target, cache string
	}{
		{"hashed url is immutable", cssHref, "public, max-age=31536000, immutable"},
		{"bare url is not cached", cssPath, "no-store"},
		{"stale hash is not cached", cssPath + "?v=old", "no-store"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := get(t, tt.target)
			h := rec.Header()
			if rec.Code != http.StatusOK || !strings.HasPrefix(h.Get("Content-Type"), "text/css") ||
				h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Cache-Control") != tt.cache ||
				h.Get("Content-Security-Policy") != wantCSP {
				t.Fatalf("%d %v", rec.Code, h)
			}
			if rec.Body.Len() == 0 || strings.Contains(rec.Body.String(), "@import") || strings.Contains(rec.Body.String(), "url(") {
				t.Fatalf("unexpected css body")
			}
		})
	}
}

func TestStaticServesNothingElse(t *testing.T) {
	for _, p := range []string{"/static/", "/static", "/static/other.css", "/static/admin.css/", "/static/../go.mod", "/static/%2e%2e/go.mod", "/static/admin.css/../x", "/Static/admin.css"} {
		if rec := get(t, p); rec.Code == http.StatusOK {
			t.Errorf("%s answered 200", p)
		}
	}
	req := httptest.NewRequest(http.MethodPost, cssPath, nil)
	req.Host = Host
	rec := httptest.NewRecorder()
	New(fakeReady{}, Deps{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d", rec.Code)
	}
}

func TestPagesLinkTheStylesheetWithoutInlineStyle(t *testing.T) {
	e := newEnv(t)
	c := signedIn(t, e)
	for _, p := range []string{"/signin", "/", "/learners", "/learners?field=username&q=ad", "/learners/" + someID, "/learners/" + someID + "/suspend"} {
		rec := e.do(http.MethodGet, p, c)
		body := rec.Body.String()
		if rec.Code != 200 || !strings.Contains(body, `href="`+cssHref+`"`) {
			t.Errorf("%s: %d, no stylesheet link", p, rec.Code)
		}
		for _, bad := range []string{"<style", " style=", "<script", "http://", "https://"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s contains %q", p, bad)
			}
		}
		if rec.Header().Get("Content-Security-Policy") != wantCSP {
			t.Errorf("%s CSP = %q", p, rec.Header().Get("Content-Security-Policy"))
		}
	}
}
