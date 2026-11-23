// Package storage is the object-store adapter (design 1.2 and 1.7): production talks to S3, the
// local stack to an S3-compatible container, and callers only see the Store interface.
package storage

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Secret is a credential that never prints itself.
type Secret string

func (Secret) String() string   { return "[redacted]" }
func (Secret) GoString() string { return "[redacted]" }

// Config says where the bucket is and how to reach it.
type Config struct {
	// Endpoint is the S3 origin the process itself talks to (scheme://host[:port]). Empty means
	// AWS S3 in Region.
	Endpoint string
	// PublicEndpoint is the origin baked into presigned URLs, which a client outside the compose
	// network must be able to reach. Empty means the same as Endpoint.
	PublicEndpoint string
	Region         string
	Bucket         string
	// AccessKey and SecretKey are static credentials; both empty means the task or instance role.
	AccessKey string
	SecretKey Secret
	// PathStyle puts the bucket in the URL path instead of the host name. Needed by local stand-ins.
	PathStyle bool
}

// Local defaults: the compose stand-in published on loopback, with its fixed throwaway credentials.
const (
	localEndpoint  = "http://localhost:9000"
	localRegion    = "us-east-1"
	localBucket    = "dojo-catalog"
	localAccessKey = "dojo-local"
	localSecretKey = "dojo-local-only-secret"
)

var bucketRE = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

// LoadConfig reads DOJO_S3_ENDPOINT, DOJO_S3_PUBLIC_ENDPOINT, DOJO_S3_REGION, DOJO_S3_BUCKET,
// DOJO_S3_ACCESS_KEY, DOJO_S3_SECRET_KEY and DOJO_S3_PATH_STYLE.
//
// Local fills every gap with the compose defaults. Outside it the bucket and the region are
// required, the endpoints must be https, and the keys are optional (empty means the IAM role) but
// never half set.
func LoadConfig(get func(string) string, local bool) (Config, error) {
	c := Config{
		Region:    get("DOJO_S3_REGION"),
		Bucket:    get("DOJO_S3_BUCKET"),
		AccessKey: get("DOJO_S3_ACCESS_KEY"),
		SecretKey: Secret(get("DOJO_S3_SECRET_KEY")),
	}
	var err error
	if c.Endpoint, err = origin(get("DOJO_S3_ENDPOINT"), "DOJO_S3_ENDPOINT", local); err != nil {
		return Config{}, err
	}
	if c.PublicEndpoint, err = origin(get("DOJO_S3_PUBLIC_ENDPOINT"), "DOJO_S3_PUBLIC_ENDPOINT", local); err != nil {
		return Config{}, err
	}
	if v := get("DOJO_S3_PATH_STYLE"); v != "" {
		if c.PathStyle, err = strconv.ParseBool(v); err != nil {
			return Config{}, fmt.Errorf("DOJO_S3_PATH_STYLE must be true or false, got %q", v)
		}
	}

	if local {
		setIf(&c.Endpoint, localEndpoint)
		// The stand-in has no wildcard DNS for bucket.host names.
		if get("DOJO_S3_PATH_STYLE") == "" {
			c.PathStyle = true
		}
		setIf(&c.Region, localRegion)
		setIf(&c.Bucket, localBucket)
		if c.AccessKey == "" && c.SecretKey == "" {
			c.AccessKey, c.SecretKey = localAccessKey, localSecretKey
		}
	} else if c.Bucket == "" || c.Region == "" {
		return Config{}, errors.New("DOJO_S3_BUCKET and DOJO_S3_REGION are required")
	}
	if !bucketRE.MatchString(c.Bucket) {
		return Config{}, fmt.Errorf("DOJO_S3_BUCKET %q is not a valid bucket name", c.Bucket)
	}
	if (c.AccessKey == "") != (c.SecretKey == "") {
		return Config{}, errors.New("DOJO_S3_ACCESS_KEY and DOJO_S3_SECRET_KEY must be set together or not at all")
	}
	if c.PublicEndpoint == "" {
		c.PublicEndpoint = c.Endpoint
	}
	return c, nil
}

func setIf(dst *string, v string) {
	if *dst == "" {
		*dst = v
	}
}

// origin accepts scheme://host[:port] only (https, or http when local), after trimming a trailing slash.
func origin(v, name string, local bool) (string, error) {
	v = strings.TrimRight(v, "/")
	if v == "" {
		return "", nil
	}
	u, err := url.Parse(v)
	if err != nil || strings.ContainsAny(v, "?#") || u.User != nil || u.Path != "" ||
		u.Hostname() == "" || strings.HasSuffix(u.Host, ":") ||
		(u.Scheme != "https" && (!local || u.Scheme != "http")) {
		return "", fmt.Errorf("%s must be a bare origin (https, or http when local)", name)
	}
	return v, nil
}

// validKey rejects keys that are empty, absolute, have empty, "." or ".." segments, hold control
// characters or exceed S3's 1024 bytes.
func validKey(k string) error {
	if k == "" || len(k) > 1024 {
		return errors.New("storage: key must be 1 to 1024 bytes")
	}
	if !utf8.ValidString(k) {
		return errors.New("storage: key is not valid UTF-8")
	}
	if strings.HasPrefix(k, StagingPrefix) {
		return errors.New("storage: the staging/ prefix is reserved")
	}
	for _, r := range k {
		if r < 0x20 || r == 0x7f || r == '\\' {
			return errors.New("storage: key holds a control character or a backslash")
		}
	}
	for _, seg := range strings.Split(k, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("storage: key %q has an empty, . or .. segment", k)
		}
	}
	return nil
}
