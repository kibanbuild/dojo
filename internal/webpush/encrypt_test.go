package webpush

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"testing"
)

func b64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// RFC 8291 appendix A.
func TestEncryptRFC8291Vector(t *testing.T) {
	asPriv, err := ecdh.P256().NewPrivateKey(b64(t, "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"))
	if err != nil {
		t.Fatal(err)
	}
	uaPub := b64(t, "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4")
	auth := b64(t, "BTBZMqHH6r4Tts7J_aSIgg")
	salt := b64(t, "DGv6ra1nlYgDCS1FRnbzlw")
	want := b64(t, "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN")

	got, err := encrypt([]byte("When I grow up, I want to be a watermelon"), uaPub, auth, asPriv, salt)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("body mismatch\n got %x\nwant %x", got, want)
	}
}

func TestEncryptRoundTrip(t *testing.T) {
	ua, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	body, err := Encrypt([]byte(`{"title":"hi"}`), ua.PublicKey().Bytes(), auth)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decrypt(body, ua, auth)
	if err != nil || string(got) != `{"title":"hi"}` {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestEncryptRejects(t *testing.T) {
	ua, _ := ecdh.P256().GenerateKey(rand.Reader)
	pub := ua.PublicKey().Bytes()
	auth := make([]byte, 16)
	tests := []struct {
		name     string
		payload  []byte
		pub, aut []byte
	}{
		{"too large", make([]byte, MaxPayload+1), pub, auth},
		{"short key", []byte("x"), pub[:64], auth},
		{"off-curve key", []byte("x"), append([]byte{4}, make([]byte, 64)...), auth},
		{"short auth", []byte("x"), pub, auth[:15]},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Encrypt(tt.payload, tt.pub, tt.aut); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	if _, err := Encrypt(make([]byte, MaxPayload), pub, auth); err != nil {
		t.Fatalf("max payload: %v", err)
	}
}
