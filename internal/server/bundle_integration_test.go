//go:build integration

package server_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kibanbuild/dojo/internal/storage"
)

// putBundle stores body in the compose object store and points the seeded ticket support-l1/c at it.
func putBundle(t *testing.T, body []byte) string {
	t.Helper()
	port := os.Getenv("DOJO_S3_PORT")
	if port == "" {
		t.Fatal("DOJO_S3_PORT is not set: run `make test-integration`")
	}
	cfg, err := storage.LoadConfig(func(k string) string {
		switch k {
		case "DOJO_S3_ENDPOINT":
			return "http://127.0.0.1:" + port
		case "DOJO_S3_ACCESS_KEY":
			return envExample(t, "DOJO_S3_ACCESS_KEY")
		case "DOJO_S3_SECRET_KEY":
			return envExample(t, "DOJO_S3_SECRET_KEY")
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
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	key := "bundles/support-l1/c/" + digest + ".tar.gz"
	if err := s.Put(context.Background(), key, bytes.NewReader(body), int64(len(body)), digest); err != nil && !strings.Contains(err.Error(), "already exists") {
		t.Fatal(err)
	}
	asOwner(t, fmt.Sprintf("UPDATE catalog_items SET bundle_key = '%s', bundle_sha256 = '%s', bundle_bytes = %d WHERE id = 'support-l1/c'",
		key, digest, len(body)))
	return digest
}

// download follows a presigned URL. The API signs for DOJO_S3_PUBLIC_ENDPOINT (localhost:9000 in the
// compose default) while the stand-in listens on a random port here: dial the real port, keep the signed Host.
func download(t *testing.T, u string) (int, []byte) {
	t.Helper()
	d := net.Dialer{Timeout: 5 * time.Second}
	c := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return d.DialContext(ctx, network, "127.0.0.1:"+os.Getenv("DOJO_S3_PORT"))
		},
	}}
	resp, err := c.Get(u)
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

func TestBundleFetchIntegration(t *testing.T) {
	freshCatalog(t)
	seedCatalog(t)
	resetLearners(t, "1001")
	body := bytes.Repeat([]byte("bundle bytes\n"), 500)
	digest := putBundle(t, body)
	signUp(t, "ada")
	tok := cliToken(t, newCLIClient(t), "ada")

	code, out, h := getAs(t, tok, "/v1/tickets/support-l1/c/bundle")
	var got struct {
		ItemID         string `json:"item_id"`
		CatalogVersion string `json:"catalog_version"`
		SHA256         string `json:"sha256"`
		Bytes          int    `json:"bytes"`
		URL            string `json:"url"`
		ExpiresAt      string `json:"expires_at"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || code != 200 {
		t.Fatalf("%d %v %s", code, err, out)
	}
	if h.Get("Cache-Control") != "no-store" || got.ItemID != "support-l1/c" || got.CatalogVersion != "a1b2c3d4e5f6" ||
		got.SHA256 != digest || got.Bytes != len(body) {
		t.Fatalf("answer: %s (%q)", out, h.Get("Cache-Control"))
	}
	exp, err := time.Parse(time.RFC3339, got.ExpiresAt)
	if err != nil || time.Until(exp) < 9*time.Minute || time.Until(exp) > 10*time.Minute+5*time.Second {
		t.Fatalf("expires_at %q (%v)", got.ExpiresAt, err)
	}
	if !strings.Contains(got.URL, "X-Amz-Expires=600") {
		t.Fatalf("presigned url is not valid 10 minutes: %s", got.URL)
	}
	dcode, data := download(t, got.URL)
	sum := sha256.Sum256(data)
	if dcode != 200 || len(data) != got.Bytes || hex.EncodeToString(sum[:]) != got.SHA256 {
		t.Fatalf("download: %d, %d bytes", dcode, len(data))
	}
}

func TestBundleErrorsIntegration(t *testing.T) {
	freshCatalog(t)
	seedCatalog(t)
	resetLearners(t, "1001")
	putBundle(t, []byte("errors bundle"))
	signUp(t, "ada")
	tok := cliToken(t, newCLIClient(t), "ada")

	if code, body, _ := getAs(t, "", "/v1/tickets/support-l1/c/bundle"); code != http.StatusUnauthorized || body != "{\"error\":\"unauthenticated\"}\n" {
		t.Fatalf("anonymous: %d %q", code, body)
	}
	for _, p := range []string{"/v1/tickets/support-l1/nope/bundle", "/v1/tickets/foundations/q/bundle", "/v1/tickets/Bad_ID/x/bundle", "/v1/tickets/support-l1/c/bundle/x"} {
		if code, body, _ := getAs(t, tok, p); code != http.StatusNotFound || body != "{\"error\":\"not_found\"}\n" {
			t.Fatalf("%s: %d %q", p, code, body)
		}
	}
	asOwner(t, "UPDATE learners SET suspended_at = now() WHERE github_id = 1001")
	if code, body, _ := getAs(t, tok, "/v1/tickets/support-l1/c/bundle"); code != http.StatusForbidden || body != "{\"error\":\"account_suspended\"}\n" {
		t.Fatalf("suspended: %d %q", code, body)
	}
}

func TestBundleRateLimitIntegration(t *testing.T) {
	freshCatalog(t)
	seedCatalog(t)
	resetLearners(t, "1001")
	putBundle(t, []byte("limit bundle"))
	signUp(t, "ada")
	tok := cliToken(t, newCLIClient(t), "ada")

	for i := 0; i < 30; i++ {
		if code, body, _ := getAs(t, tok, "/v1/tickets/support-l1/c/bundle"); code != 200 {
			t.Fatalf("request %d: %d %s", i+1, code, body)
		}
	}
	code, body, h := getAs(t, tok, "/v1/tickets/support-l1/c/bundle")
	if code != http.StatusTooManyRequests || body != "{\"error\":\"rate_limited\"}\n" || h.Get("Retry-After") == "" {
		t.Fatalf("31st: %d %q %q", code, body, h.Get("Retry-After"))
	}
	if code, _, _ := getAs(t, tok, "/v1/catalog"); code != 200 {
		t.Fatalf("catalog shares the bundle budget: %d", code)
	}
}
