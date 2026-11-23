package storage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Errors a caller can test with errors.Is.
var (
	ErrNotFound = errors.New("storage: object not found")
	// ErrIntegrity means the bytes read did not match the declared size or sha256; nothing is stored under the key.
	ErrIntegrity = errors.New("storage: content does not match the declared size or sha256")
	// ErrExists means the key is taken; objects are immutable, so Put never overwrites.
	ErrExists = errors.New("storage: object already exists")
)

// StagingPrefix holds uploads not yet verified. Callers cannot use it in a key.
const StagingPrefix = "staging/"

// MaxPresignTTL is S3's own limit for a SigV4 presigned URL.
const MaxPresignTTL = 7 * 24 * time.Hour

// Object is what Stat reports.
type Object struct {
	Size int64
	// SHA256 is the lowercase hex digest recorded at Put, empty for objects put by something else.
	SHA256 string
}

// Store is the object-store port. Keys are slash-separated, with no empty, "." or ".." segment.
type Store interface {
	// Put stores size bytes from r under key. sha256Hex is the expected digest of those bytes.
	Put(ctx context.Context, key string, r io.Reader, size int64, sha256Hex string) error
	// PresignGet returns a URL that downloads key with no credentials until ttl runs out. The URL
	// points at the public endpoint, so a client outside the compose network can use it.
	PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)
	// Stat reports the object, or ErrNotFound.
	Stat(ctx context.Context, key string) (Object, error)
}

// S3 implements Store on any S3-compatible service through minio-go.
type S3 struct {
	bucket string
	data   *minio.Client // the endpoint this process reaches
	public *minio.Client // same service, the endpoint presigned URLs carry; only signs
}

var _ Store = (*S3)(nil)

// New builds the clients. It makes no network call. For a bare s3.<region> endpoint minio-go uses
// the dualstack host (s3.dualstack.<region>.amazonaws.com): confirm presigned URLs against a real
// bucket before pre.
func New(c Config) (*S3, error) {
	creds := credentials.NewIAM("")
	if c.AccessKey != "" {
		creds = credentials.NewStaticV4(c.AccessKey, string(c.SecretKey), "")
	}
	data, err := newClient(c, c.Endpoint, creds)
	if err != nil {
		return nil, err
	}
	public := data
	if c.PublicEndpoint != c.Endpoint {
		if public, err = newClient(c, c.PublicEndpoint, creds); err != nil {
			return nil, err
		}
	}
	return &S3{bucket: c.Bucket, data: data, public: public}, nil
}

func newClient(c Config, endpoint string, creds *credentials.Credentials) (*minio.Client, error) {
	host, secure := "s3."+c.Region+".amazonaws.com", true
	if endpoint != "" {
		u, err := url.Parse(endpoint)
		if err != nil {
			return nil, fmt.Errorf("storage: endpoint: %w", err)
		}
		host, secure = u.Host, u.Scheme == "https"
	}
	lookup := minio.BucketLookupDNS
	if c.PathStyle {
		lookup = minio.BucketLookupPath
	}
	// Region is always set: with it the client never asks the service where the bucket is, so
	// signing a URL needs no network.
	return minio.New(host, &minio.Options{Creds: creds, Secure: secure, Region: c.Region, BucketLookup: lookup})
}

func (s *S3) Put(ctx context.Context, key string, r io.Reader, size int64, sha256Hex string) error {
	if err := validKey(key); err != nil {
		return err
	}
	want := strings.ToLower(sha256Hex)
	if b, err := hex.DecodeString(want); err != nil || len(b) != sha256.Size {
		return errors.New("storage: sha256 must be 64 hex characters")
	}
	if size < 0 {
		return errors.New("storage: size must be known")
	}
	// Unverified bytes never touch the final key: stage, verify, then copy.
	var rnd [16]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return fmt.Errorf("storage: staging name: %w", err)
	}
	staging := StagingPrefix + hex.EncodeToString(rnd[:])
	hr := &hashReader{r: r, h: sha256.New()}
	_, err := s.data.PutObject(ctx, s.bucket, staging, hr, size, minio.PutObjectOptions{
		UserMetadata: map[string]string{"sha256": want},
	})
	defer s.dropStaging(ctx, staging)
	if err != nil {
		return fmt.Errorf("storage: put %q: %w", key, err)
	}
	if hr.n != size || hex.EncodeToString(hr.h.Sum(nil)) != want {
		return ErrIntegrity
	}
	// S3 has no conditional CopyObject, so this check can race with a concurrent Put of the same
	// key. Bundle keys are content-addressed and immutable, so both writers carry the same bytes.
	switch _, err := s.Stat(ctx, key); {
	case err == nil:
		return ErrExists
	case !errors.Is(err, ErrNotFound):
		return err
	}
	_, err = s.data.CopyObject(ctx,
		minio.CopyDestOptions{Bucket: s.bucket, Object: key},
		minio.CopySrcOptions{Bucket: s.bucket, Object: staging})
	if err != nil {
		return fmt.Errorf("storage: publish %q: %w", key, err)
	}
	return nil
}

// dropStaging removes a staging object. A failure is logged, not fatal: the object is private and
// unreferenced, and SweepStaging (plus a lifecycle rule on the staging/ prefix) collects it later.
func (s *S3) dropStaging(ctx context.Context, staging string) {
	if err := s.data.RemoveObject(context.WithoutCancel(ctx), s.bucket, staging, minio.RemoveObjectOptions{}); err != nil {
		slog.Warn("storage: staging object not removed", "key", staging, "error", err)
	}
}

// SweepStaging removes staging objects older than olderThan, left by a crashed Put or a failed
// removal, and reports how many it removed. Production also expires the prefix with a lifecycle rule.
func (s *S3) SweepStaging(ctx context.Context, olderThan time.Duration) (int, error) {
	cutoff := time.Now().Add(-olderThan)
	n := 0
	for o := range s.data.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: StagingPrefix, Recursive: true}) {
		if o.Err != nil {
			return n, fmt.Errorf("storage: sweep: %w", o.Err)
		}
		if o.LastModified.After(cutoff) {
			continue
		}
		if err := s.data.RemoveObject(ctx, s.bucket, o.Key, minio.RemoveObjectOptions{}); err != nil {
			return n, fmt.Errorf("storage: sweep %q: %w", o.Key, err)
		}
		n++
	}
	return n, nil
}

func (s *S3) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if err := validKey(key); err != nil {
		return "", err
	}
	if ttl < time.Second || ttl > MaxPresignTTL {
		return "", fmt.Errorf("storage: ttl must be between 1s and %s", MaxPresignTTL)
	}
	u, err := s.public.PresignedGetObject(ctx, s.bucket, key, ttl, nil)
	if err != nil {
		return "", fmt.Errorf("storage: presign %q: %w", key, err)
	}
	return u.String(), nil
}

func (s *S3) Stat(ctx context.Context, key string) (Object, error) {
	if err := validKey(key); err != nil {
		return Object{}, err
	}
	info, err := s.data.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		if minio.ToErrorResponse(err).Code == minio.NoSuchKey {
			return Object{}, ErrNotFound
		}
		return Object{}, fmt.Errorf("storage: stat %q: %w", key, err)
	}
	o := Object{Size: info.Size}
	for k, v := range info.UserMetadata {
		if strings.EqualFold(k, "sha256") {
			o.SHA256 = v
		}
	}
	return o, nil
}

// hashReader counts and hashes what passes through.
type hashReader struct {
	r io.Reader
	h hash.Hash
	n int64
}

func (h *hashReader) Read(p []byte) (int, error) {
	n, err := h.r.Read(p)
	h.h.Write(p[:n])
	h.n += int64(n)
	return n, err
}
