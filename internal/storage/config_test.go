package storage

import (
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadConfigLocalDefaults(t *testing.T) {
	c, err := LoadConfig(env(nil), true)
	if err != nil {
		t.Fatal(err)
	}
	if c.Endpoint != "http://localhost:9000" || c.PublicEndpoint != c.Endpoint || c.Bucket != "dojo-catalog" ||
		c.Region != "us-east-1" || !c.PathStyle || c.AccessKey == "" || c.SecretKey == "" {
		t.Fatalf("defaults: %+v", c)
	}
}

func TestLoadConfigPublicEndpointDefaultsToEndpoint(t *testing.T) {
	c, err := LoadConfig(env(map[string]string{"DOJO_S3_ENDPOINT": "http://rustfs:9000"}), true)
	if err != nil {
		t.Fatal(err)
	}
	if c.PublicEndpoint != "http://rustfs:9000" {
		t.Fatalf("public endpoint %q", c.PublicEndpoint)
	}
	c, err = LoadConfig(env(map[string]string{"DOJO_S3_ENDPOINT": "http://rustfs:9000", "DOJO_S3_PUBLIC_ENDPOINT": "http://localhost:9000/"}), true)
	if err != nil {
		t.Fatal(err)
	}
	if c.Endpoint != "http://rustfs:9000" || c.PublicEndpoint != "http://localhost:9000" {
		t.Fatalf("endpoints %q %q", c.Endpoint, c.PublicEndpoint)
	}
}

func TestLoadConfigRemote(t *testing.T) {
	base := map[string]string{"DOJO_S3_BUCKET": "dojo-pro-catalog", "DOJO_S3_REGION": "eu-west-1"}
	with := func(kv ...string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{"aws with task role", with(), ""},
		{"static keys", with("DOJO_S3_ACCESS_KEY", "a", "DOJO_S3_SECRET_KEY", "b"), ""},
		{"https endpoint", with("DOJO_S3_ENDPOINT", "https://s3.example.com", "DOJO_S3_PATH_STYLE", "true"), ""},
		{"missing bucket", map[string]string{"DOJO_S3_REGION": "eu-west-1"}, "DOJO_S3_BUCKET"},
		{"missing region", map[string]string{"DOJO_S3_BUCKET": "b"}, "DOJO_S3_REGION"},
		{"access key alone", with("DOJO_S3_ACCESS_KEY", "a"), "DOJO_S3_ACCESS_KEY and DOJO_S3_SECRET_KEY"},
		{"secret key alone", with("DOJO_S3_SECRET_KEY", "b"), "DOJO_S3_ACCESS_KEY and DOJO_S3_SECRET_KEY"},
		{"http endpoint", with("DOJO_S3_ENDPOINT", "http://s3.example.com"), "DOJO_S3_ENDPOINT"},
		{"http public endpoint", with("DOJO_S3_PUBLIC_ENDPOINT", "http://s3.example.com"), "DOJO_S3_PUBLIC_ENDPOINT"},
		{"endpoint with path", with("DOJO_S3_ENDPOINT", "https://s3.example.com/x"), "DOJO_S3_ENDPOINT"},
		{"endpoint with userinfo", with("DOJO_S3_ENDPOINT", "https://u:p@s3.example.com"), "DOJO_S3_ENDPOINT"},
		{"bad path style", with("DOJO_S3_PATH_STYLE", "maybe"), "DOJO_S3_PATH_STYLE"},
		{"bad bucket", with("DOJO_S3_BUCKET", "Not_A_Bucket"), "DOJO_S3_BUCKET"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadConfig(env(tt.env), false)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("error %v, want it to name %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoadConfigRemoteDefaultsToVirtualHostedAWS(t *testing.T) {
	c, err := LoadConfig(env(map[string]string{"DOJO_S3_BUCKET": "dojo-bucket", "DOJO_S3_REGION": "eu-west-1"}), false)
	if err != nil {
		t.Fatal(err)
	}
	if c.Endpoint != "" || c.PublicEndpoint != "" || c.PathStyle {
		t.Fatalf("got %+v", c)
	}
}

func TestSecretIsRedacted(t *testing.T) {
	c, err := LoadConfig(env(nil), true)
	if err != nil {
		t.Fatal(err)
	}
	if s := (strings.Join([]string{c.SecretKey.String(), c.SecretKey.GoString()}, "|")); strings.Contains(s, string(c.SecretKey)) {
		t.Fatalf("secret leaks: %s", s)
	}
}

func TestValidKey(t *testing.T) {
	for _, k := range []string{"catalog/v1/ticket.tar.gz", "a", "b/c-d_e.f"} {
		if err := validKey(k); err != nil {
			t.Errorf("%q: %v", k, err)
		}
	}
	for _, k := range []string{"", "/abs", "a//b", "a/../b", "..", "a/./b", "trail/", "ctl\x00x", "new\nline", "a\\b", "bad\xffutf8", "staging/x", strings.Repeat("x", 1025)} {
		if err := validKey(k); err == nil {
			t.Errorf("%q accepted", k)
		}
	}
}
