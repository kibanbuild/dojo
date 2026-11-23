// Package github is the API's client for GitHub's OAuth web flow, the user endpoints and token revoke.
package github

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
)

// Secret is a credential that prints as [redacted] under fmt and slog.
type Secret string

func (Secret) String() string   { return "[redacted]" }
func (Secret) GoString() string { return "[redacted]" }

// MarshalText keeps the secret out of JSON and other text encodings.
func (Secret) MarshalText() ([]byte, error) { return []byte("[redacted]"), nil }

// LogValue implements slog.LogValuer.
func (Secret) LogValue() slog.Value { return slog.StringValue("[redacted]") }

// Config holds the OAuth App credentials and the three GitHub origins.
// The origins differ locally: the browser reaches the fake at localhost, the API container at its service name.
type Config struct {
	ClientID     string
	ClientSecret Secret
	AuthorizeURL string // origin the browser is sent to (https://github.com)
	OAuthURL     string // origin of the server-side token exchange (https://github.com)
	APIURL       string // origin of the REST API (https://api.github.com)
	// Scopes is the space-separated scope list asked for at sign-in; empty means the public app's Scopes.
	Scopes string
}

// String and LogValue keep the secret out of logs and error output.
func (c Config) String() string {
	return fmt.Sprintf("github.Config{client_id=%s, secret=[redacted], authorize=%s, oauth=%s, api=%s}",
		c.ClientID, c.AuthorizeURL, c.OAuthURL, c.APIURL)
}

// GoString implements fmt.GoStringer.
func (c Config) GoString() string { return c.String() }

// LogValue implements slog.LogValuer.
func (c Config) LogValue() slog.Value { return slog.StringValue(c.String()) }

// LoadConfig reads GITHUB_CLIENT_ID, GITHUB_CLIENT_SECRET and the optional origins
// GITHUB_AUTHORIZE_URL, GITHUB_OAUTH_URL and GITHUB_API_URL (default: the real GitHub).
// Outside the local environment the origins must be https, so the secret never travels in clear.
func LoadConfig(get func(string) string, local bool) (Config, error) {
	c := Config{ClientID: get("GITHUB_CLIENT_ID"), ClientSecret: Secret(get("GITHUB_CLIENT_SECRET"))}
	if c.ClientID == "" || c.ClientSecret == "" {
		return Config{}, errors.New("GITHUB_CLIENT_ID and GITHUB_CLIENT_SECRET are required")
	}
	for _, o := range []struct {
		dst         *string
		name, deflt string
	}{
		{&c.AuthorizeURL, "GITHUB_AUTHORIZE_URL", "https://github.com"},
		{&c.OAuthURL, "GITHUB_OAUTH_URL", "https://github.com"},
		{&c.APIURL, "GITHUB_API_URL", "https://api.github.com"},
	} {
		v := get(o.name)
		if v == "" {
			v = o.deflt
		}
		v = strings.TrimRight(v, "/")
		if !bareOrigin(v, local) {
			return Config{}, fmt.Errorf("%s must be a bare origin (https, or http when local)", o.name)
		}
		*o.dst = v
	}
	return c, nil
}

// LoadAPIConfig reads only GITHUB_API_URL (default: the real GitHub) for the unauthenticated
// lookups of the bootstrap command, which holds no OAuth credentials.
func LoadAPIConfig(get func(string) string, local bool) (Config, error) {
	v := get("GITHUB_API_URL")
	if v == "" {
		v = "https://api.github.com"
	}
	v = strings.TrimRight(v, "/")
	if !bareOrigin(v, local) {
		return Config{}, errors.New("GITHUB_API_URL must be a bare origin (https, or http when local)")
	}
	return Config{APIURL: v}, nil
}

// bareOrigin accepts scheme://host[:port] only: no userinfo, path, query, fragment or empty port.
func bareOrigin(v string, local bool) bool {
	u, err := url.Parse(v)
	if err != nil || strings.ContainsAny(v, "?#") || u.User != nil || u.ForceQuery ||
		u.Hostname() == "" || strings.HasSuffix(u.Host, ":") || u.Path != "" {
		return false
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return false
		}
	}
	return u.Scheme == "https" || (local && u.Scheme == "http")
}
