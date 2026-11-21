package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeReady struct{ err error }

func (f fakeReady) Ready(context.Context) error { return f.err }

func TestHealth(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		ready      error
		wantStatus int
		wantBody   string
	}{
		{"ok", http.MethodGet, nil, http.StatusOK, `{"status":"ok"}`},
		{"database down", http.MethodGet, errors.New("connection refused: secret-host"), http.StatusServiceUnavailable, `{"status":"unavailable"}`},
		{"post not allowed", http.MethodPost, nil, http.StatusMethodNotAllowed, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := New(fakeReady{err: tt.ready})
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tt.method, "/v1/health", nil))
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantBody != "" {
				if got := rec.Body.String(); got != tt.wantBody+"\n" {
					t.Fatalf("body = %q, want %q", got, tt.wantBody+"\n")
				}
				if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
					t.Fatalf("content-type = %q", ct)
				}
			}
		})
	}
}

type slowReady struct{}

func (slowReady) Ready(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestHealthTimesOut(t *testing.T) {
	h := New(slowReady{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/health", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}
