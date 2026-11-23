package storage

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeS3 answers just enough of the S3 API for Put: every request is recorded, HEAD finds nothing,
// and DELETE is refused (403 is not retried by the client).
type fakeS3 struct {
	mu   sync.Mutex
	reqs []string
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.reqs = append(f.reqs, r.Method+" "+r.URL.Path)
	f.mu.Unlock()
	switch {
	case r.Method == http.MethodHead:
		w.WriteHeader(http.StatusNotFound)
	case r.Method == http.MethodDelete:
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<Error><Code>AccessDenied</Code><Message>no</Message></Error>`))
	case r.Header.Get("X-Amz-Copy-Source") != "":
		_, _ = w.Write([]byte(`<CopyObjectResult><ETag>"e"</ETag><LastModified>2026-01-01T00:00:00.000Z</LastModified></CopyObjectResult>`))
	default:
		_, _ = bytes.NewBuffer(nil).ReadFrom(r.Body)
		w.Header().Set("ETag", `"e"`)
	}
}

func fakeStore(t *testing.T) (*S3, *fakeS3) {
	t.Helper()
	f := &fakeS3{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c, err := LoadConfig(env(map[string]string{"DOJO_S3_ENDPOINT": srv.URL}), true)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	return s, f
}

func (f *fakeS3) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.reqs {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
}

func logTo(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}

const helloSHA = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"

func TestFailedStagingRemovalIsLoggedNotFatal(t *testing.T) {
	s, f := fakeStore(t)
	logs := logTo(t)
	if err := s.Put(context.Background(), "a/b", strings.NewReader("hello"), 5, helloSHA); err != nil {
		t.Fatalf("a refused staging delete failed the Put: %v", err)
	}
	if f.count("PUT /dojo-catalog/a/b") != 1 || f.count("DELETE /dojo-catalog/staging/") != 1 {
		t.Fatalf("requests: %v", f.reqs)
	}
	if !strings.Contains(logs.String(), "staging object not removed") {
		t.Fatalf("no warning logged: %s", logs)
	}
}

func TestBadDigestNeverReachesTheFinalKeyEvenWhenStagingRemovalFails(t *testing.T) {
	s, f := fakeStore(t)
	logs := logTo(t)
	err := s.Put(context.Background(), "a/b", strings.NewReader("hello"), 5, strings.Repeat("0", 64))
	if !errors.Is(err, ErrIntegrity) {
		t.Fatalf("got %v, want ErrIntegrity", err)
	}
	if f.count("PUT /dojo-catalog/a/b") != 0 {
		t.Fatalf("the final key was written: %v", f.reqs)
	}
	if !strings.Contains(logs.String(), "staging object not removed") {
		t.Fatalf("no warning logged: %s", logs)
	}
}
