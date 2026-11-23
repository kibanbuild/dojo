package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeReady struct{ err error }

func (f fakeReady) Ready(context.Context) error { return f.err }

func TestHostCheck(t *testing.T) {
	tests := []struct {
		name       string
		host       string
		wantStatus int
	}{
		{"exact host", "127.0.0.1:8090", http.StatusOK},
		{"missing host", "", http.StatusMisdirectedRequest},
		{"localhost", "localhost:8090", http.StatusMisdirectedRequest},
		{"other port", "127.0.0.1:8080", http.StatusMisdirectedRequest},
		{"no port", "127.0.0.1", http.StatusMisdirectedRequest},
		{"rebinding name", "evil.example.com:8090", http.StatusMisdirectedRequest},
		{"suffix trick", "127.0.0.1:8090.evil.example.com", http.StatusMisdirectedRequest},
		{"upper case name", "LOCALHOST:8090", http.StatusMisdirectedRequest},
		{"ipv6 loopback", "[::1]:8090", http.StatusMisdirectedRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := New(fakeReady{}, Deps{})
			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			req.Host = tt.host
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantStatus != http.StatusOK && rec.Body.Len() != 0 {
				t.Fatalf("a refused request got a body: %q", rec.Body.String())
			}
		})
	}
}

// Every path is behind the host check, including ones that do not exist: a rebinding page must not
// learn which routes there are.
func TestHostCheckCoversUnknownPaths(t *testing.T) {
	h := New(fakeReady{}, Deps{})
	for _, path := range []string{"/", "/nope", "/healthz/"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "evil.example.com"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusMisdirectedRequest || rec.Body.Len() != 0 {
			t.Errorf("%s: got %d %q", path, rec.Code, rec.Body.String())
		}
	}
}

func TestHealth(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		ready      error
		wantStatus int
		wantBody   string
	}{
		{"ok", http.MethodGet, "/healthz", nil, http.StatusOK, "{\"status\":\"ok\"}\n"},
		{"database down", http.MethodGet, "/healthz", errors.New("connection refused: secret-host"), http.StatusServiceUnavailable, "{\"status\":\"unavailable\"}\n"},
		{"post not allowed", http.MethodPost, "/healthz", nil, http.StatusMethodNotAllowed, ""},
		{"unknown path", http.MethodGet, "/nope", nil, http.StatusNotFound, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			req.Host = Host
			rec := httptest.NewRecorder()
			New(fakeReady{err: tt.ready}, Deps{}).ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantBody != "" && rec.Body.String() != tt.wantBody {
				t.Fatalf("body = %q, want %q", rec.Body.String(), tt.wantBody)
			}
		})
	}
}
