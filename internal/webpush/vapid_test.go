package webpush

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/asn1"
	"encoding/json"
	"math/big"
	"regexp"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)

func TestVAPIDHeader(t *testing.T) {
	v, err := ParseVAPID(ExampleKey)
	if err != nil {
		t.Fatal(err)
	}
	h, err := v.Header("https://fcm.googleapis.com", "mailto:hello@example.test", now)
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`^vapid t=([\w-]+)\.([\w-]+)\.([\w-]+), k=([\w-]+)$`).FindStringSubmatch(h)
	if m == nil {
		t.Fatalf("header shape: %s", h)
	}
	if string(b64(t, m[1])) != `{"typ":"JWT","alg":"ES256"}` {
		t.Errorf("header %s", b64(t, m[1]))
	}
	var c struct {
		Aud, Sub string
		Exp      int64
	}
	if err := json.Unmarshal(b64(t, m[2]), &c); err != nil {
		t.Fatal(err)
	}
	if c.Aud != "https://fcm.googleapis.com" || c.Sub != "mailto:hello@example.test" || c.Exp != now.Add(12*time.Hour).Unix() {
		t.Errorf("claims %+v", c)
	}
	k := b64(t, m[4])
	if len(k) != 65 || string(k) != string(v.PublicKey()) {
		t.Fatalf("k %x", k)
	}
	sig := b64(t, m[3])
	if len(sig) != 64 {
		t.Fatalf("signature is %d bytes", len(sig))
	}
	der, _ := asn1.Marshal(struct{ R, S *big.Int }{new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])})
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), k)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(m[1] + "." + m[2]))
	if !ecdsa.VerifyASN1(pub, sum[:], der) {
		t.Fatal("signature does not verify")
	}
}

func TestParseVAPID(t *testing.T) {
	for _, bad := range []string{"", "not base64!", "AAAA", strings.Repeat("A", 43)} {
		if _, err := ParseVAPID(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	v, err := ParseVAPID(ExampleKey)
	if err != nil || !v.IsExample() {
		t.Fatalf("%v", err)
	}
	other := "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"
	if v, err := ParseVAPID(other); err != nil || v.IsExample() {
		t.Fatalf("%v", err)
	}
}

func TestExampleKeyIsAValidFixedKey(t *testing.T) {
	if ExampleKey != "q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94" {
		t.Fatal("ExampleKey changed; every local .env that carries it must change with it")
	}
}
