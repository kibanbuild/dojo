//go:build integration

package storage_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kibanbuild/dojo/internal/storage"
)

func must(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Fatalf("%s is not set: run `make test-integration`", name)
	}
	return v
}

// newStore talks to the compose object store on its published loopback port and signs URLs for
// "localhost", a different name than the one it connects with, as the API does with its public endpoint.
func newStore(t *testing.T) (*storage.S3, storage.Config) {
	t.Helper()
	port := must(t, "DOJO_S3_PORT")
	cfg, err := storage.LoadConfig(func(k string) string {
		switch k {
		case "DOJO_S3_ENDPOINT":
			return "http://127.0.0.1:" + port
		case "DOJO_S3_PUBLIC_ENDPOINT":
			return "http://localhost:" + port
		case "DOJO_S3_ACCESS_KEY", "DOJO_S3_SECRET_KEY":
			return must(t, k)
		}
		return ""
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	s, err := storage.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s, cfg
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func get(t *testing.T, u string) (int, []byte) {
	t.Helper()
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, b
}

func TestPutPresignDownload(t *testing.T) {
	s, cfg := newStore(t)
	ctx := context.Background()
	body := bytes.Repeat([]byte("ticket bundle\n"), 1000)
	key := "it/" + digest(body)[:12] + "/bundle.tar.gz"

	if err := s.Put(ctx, key, bytes.NewReader(body), int64(len(body)), digest(body)); err != nil {
		t.Fatal(err)
	}
	obj, err := s.Stat(ctx, key)
	if err != nil || obj.Size != int64(len(body)) || obj.SHA256 != digest(body) {
		t.Fatalf("stat: %+v %v", obj, err)
	}

	u, err := s.PresignGet(ctx, key, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	pu, err := url.Parse(u)
	if err != nil || pu.Host != strings.TrimPrefix(cfg.PublicEndpoint, "http://") {
		t.Fatalf("presigned host %q, want the public endpoint %q (%v)", pu.Host, cfg.PublicEndpoint, err)
	}
	if code, got := get(t, u); code != http.StatusOK || !bytes.Equal(got, body) {
		t.Fatalf("download: %d, %d bytes", code, len(got))
	}
}

func TestPresignedURLRefusals(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	body := []byte("refusals")
	key := "it/refusals.txt"
	if err := s.Put(ctx, key, bytes.NewReader(body), int64(len(body)), digest(body)); err != nil {
		t.Fatal(err)
	}

	t.Run("expired", func(t *testing.T) {
		u, err := s.PresignGet(ctx, key, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(2500 * time.Millisecond)
		if code, _ := get(t, u); code != http.StatusForbidden {
			t.Fatalf("expired URL got %d, want 403", code)
		}
	})
	t.Run("forged signature", func(t *testing.T) {
		u, err := s.PresignGet(ctx, key, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		pu, _ := url.Parse(u)
		q := pu.Query()
		sig := q.Get("X-Amz-Signature")
		if sig == "" {
			t.Fatalf("no signature in %q", u)
		}
		flipped := "0"
		if sig[0] == '0' {
			flipped = "1"
		}
		q.Set("X-Amz-Signature", flipped+sig[1:])
		pu.RawQuery = q.Encode()
		if code, _ := get(t, pu.String()); code != http.StatusForbidden {
			t.Fatalf("forged URL got %d, want 403", code)
		}
	})
	t.Run("another key", func(t *testing.T) {
		u, err := s.PresignGet(ctx, key, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if code, _ := get(t, strings.Replace(u, "refusals.txt", "other.txt", 1)); code != http.StatusForbidden {
			t.Fatalf("URL reused for another key got %d, want 403", code)
		}
	})
	t.Run("no signature, private bucket", func(t *testing.T) {
		u, err := s.PresignGet(ctx, key, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if code, _ := get(t, strings.SplitN(u, "?", 2)[0]); code != http.StatusForbidden {
			t.Fatalf("anonymous GET got %d, want 403", code)
		}
	})
}

func TestPutRejectsWrongDigestAndRemovesTheObject(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	key := "it/bad-digest.txt"
	err := s.Put(ctx, key, strings.NewReader("actual"), 6, digest([]byte("claimed")))
	if !errors.Is(err, storage.ErrIntegrity) {
		t.Fatalf("got %v, want ErrIntegrity", err)
	}
	if _, err := s.Stat(ctx, key); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("object left behind: %v", err)
	}
}

func noStaging(t *testing.T, s *storage.S3) {
	t.Helper()
	if n, err := s.SweepStaging(context.Background(), 0); err != nil || n != 0 {
		t.Fatalf("staging objects left behind: %d, %v", n, err)
	}
}

func TestBadPutOverGoodObjectLeavesItIntact(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	good := []byte("the good bytes")
	key := "it/good-then-bad.txt"
	if err := s.Put(ctx, key, bytes.NewReader(good), int64(len(good)), digest(good)); err != nil {
		t.Fatal(err)
	}
	err := s.Put(ctx, key, strings.NewReader("evil bytes"), 10, digest([]byte("claimed")))
	if !errors.Is(err, storage.ErrIntegrity) {
		t.Fatalf("got %v, want ErrIntegrity", err)
	}
	u, err := s.PresignGet(ctx, key, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if code, got := get(t, u); code != http.StatusOK || !bytes.Equal(got, good) {
		t.Fatalf("object damaged: %d %q", code, got)
	}
	if obj, err := s.Stat(ctx, key); err != nil || obj.SHA256 != digest(good) {
		t.Fatalf("stat: %+v %v", obj, err)
	}
	noStaging(t, s)
}

func TestPutToExistingKeyReturnsErrExists(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	first, second := []byte("first"), []byte("second")
	key := "it/immutable.txt"
	if err := s.Put(ctx, key, bytes.NewReader(first), int64(len(first)), digest(first)); err != nil {
		t.Fatal(err)
	}
	err := s.Put(ctx, key, bytes.NewReader(second), int64(len(second)), digest(second))
	if !errors.Is(err, storage.ErrExists) {
		t.Fatalf("got %v, want ErrExists", err)
	}
	if obj, err := s.Stat(ctx, key); err != nil || obj.SHA256 != digest(first) {
		t.Fatalf("object replaced: %+v %v", obj, err)
	}
	noStaging(t, s)
}

func TestStagingIsEmptyAfterSuccess(t *testing.T) {
	s, _ := newStore(t)
	b := []byte("clean")
	if err := s.Put(context.Background(), "it/clean.txt", bytes.NewReader(b), int64(len(b)), digest(b)); err != nil {
		t.Fatal(err)
	}
	noStaging(t, s)
}

func TestPutRejectsShortBody(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	key := "it/short.txt"
	if err := s.Put(ctx, key, strings.NewReader("abc"), 10, digest([]byte("abc"))); err == nil {
		t.Fatal("a body shorter than the declared size was accepted")
	}
	if _, err := s.Stat(ctx, key); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("object left behind: %v", err)
	}
	noStaging(t, s)
}

func TestStatMissing(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.Stat(context.Background(), "it/never-written"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}
