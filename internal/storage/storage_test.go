package storage

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The arguments are refused before any request, so no service is needed.
func TestArgumentsAreValidatedBeforeTheNetwork(t *testing.T) {
	c, err := LoadConfig(env(nil), true)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	good := strings.Repeat("a", 64)
	puts := []struct {
		name, key, sha string
		size           int64
	}{
		{"bad key", "../x", good, 1},
		{"short sha", "k", "abc", 1},
		{"non-hex sha", "k", strings.Repeat("z", 64), 1},
		{"unknown size", "k", good, -1},
	}
	for _, p := range puts {
		if err := s.Put(ctx, p.key, strings.NewReader("x"), p.size, p.sha); err == nil {
			t.Errorf("put %s accepted", p.name)
		}
	}
	for _, ttl := range []time.Duration{0, -time.Minute, MaxPresignTTL + time.Second} {
		if _, err := s.PresignGet(ctx, "k", ttl); err == nil {
			t.Errorf("ttl %s accepted", ttl)
		}
	}
	if _, err := s.PresignGet(ctx, "a//b", time.Minute); err == nil {
		t.Error("bad key presigned")
	}
	if _, err := s.Stat(ctx, ""); err == nil {
		t.Error("empty key statted")
	}
}

// Signing needs no network: the region is set, so the client never looks the bucket up.
func TestPresignUsesThePublicEndpointOffline(t *testing.T) {
	c, err := LoadConfig(env(map[string]string{
		"DOJO_S3_ENDPOINT":        "http://s3:9000",
		"DOJO_S3_PUBLIC_ENDPOINT": "http://localhost:9000",
	}), true)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.PresignGet(context.Background(), "a/b.tar", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(u, "http://localhost:9000/dojo-catalog/a/b.tar?") || !strings.Contains(u, "X-Amz-Signature=") {
		t.Fatalf("got %s", u)
	}
}

func TestAWSDefaultsToVirtualHostedRegionalEndpoint(t *testing.T) {
	s, err := New(Config{Region: "eu-west-1", Bucket: "dojo-pro-catalog", AccessKey: "a", SecretKey: "b"})
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.PresignGet(context.Background(), "k", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// minio-go picks the dualstack host of the region for a bare s3.<region> endpoint.
	if !strings.HasPrefix(u, "https://dojo-pro-catalog.s3.") || !strings.Contains(u, ".eu-west-1.amazonaws.com/k?") {
		t.Fatalf("got %s", u)
	}
}
