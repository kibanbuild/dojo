// Package webpush sends Web Push messages: RFC 8291 encryption, RFC 8292 VAPID, and an endpoint
// allowlist with a connect-time dial guard. Standard library only.
package webpush

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

// MaxPayload is the largest plaintext Encrypt takes (design 3.10).
const MaxPayload = 3000

const recordSize = 4096

// Encrypt returns the aes128gcm body (RFC 8188 header plus one record) for a subscription's
// p256dh public key (65 bytes) and auth secret (16 bytes).
func Encrypt(payload, uaPublic, auth []byte) ([]byte, error) {
	asPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	return encrypt(payload, uaPublic, auth, asPriv, salt)
}

func encrypt(payload, uaPublic, auth []byte, asPriv *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	if len(payload) > MaxPayload {
		return nil, errors.New("webpush: payload too large")
	}
	if len(auth) != 16 {
		return nil, errors.New("webpush: auth secret must be 16 bytes")
	}
	uaPub, err := ecdh.P256().NewPublicKey(uaPublic)
	if err != nil {
		return nil, fmt.Errorf("webpush: bad p256dh key: %w", err)
	}
	secret, err := asPriv.ECDH(uaPub)
	if err != nil {
		return nil, err
	}
	asPublic := asPriv.PublicKey().Bytes()
	cek, nonce, err := deriveKeys(secret, auth, salt, uaPublic, asPublic)
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(cek)
	if err != nil {
		return nil, err
	}
	header := make([]byte, 0, 21+len(asPublic))
	header = append(header, salt...)
	header = binary.BigEndian.AppendUint32(header, recordSize)
	header = append(header, 65) // uncompressed P-256 point
	header = append(header, asPublic...)
	// 0x02 marks the last record (RFC 8188 section 2).
	return gcm.Seal(header, nonce, append(append([]byte{}, payload...), 2), nil), nil
}

func deriveKeys(secret, auth, salt, uaPublic, asPublic []byte) (cek, nonce []byte, err error) {
	prkKey, err := hkdf.Extract(sha256.New, secret, auth)
	if err != nil {
		return nil, nil, err
	}
	info := append(append([]byte("WebPush: info\x00"), uaPublic...), asPublic...)
	ikm, err := hkdf.Expand(sha256.New, prkKey, string(info), 32)
	if err != nil {
		return nil, nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, nil, err
	}
	if cek, err = hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16); err != nil {
		return nil, nil, err
	}
	nonce, err = hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	return cek, nonce, err
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
