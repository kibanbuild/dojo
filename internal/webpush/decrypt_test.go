package webpush

import (
	"crypto/ecdh"
	"errors"
)

// decrypt is the browser's side.
func decrypt(body []byte, uaPriv *ecdh.PrivateKey, auth []byte) ([]byte, error) {
	if len(body) < 86 || body[20] != 65 {
		return nil, errors.New("webpush: bad header")
	}
	salt, asPublic, ct := body[:16], body[21:86], body[86:]
	asPub, err := ecdh.P256().NewPublicKey(asPublic)
	if err != nil {
		return nil, err
	}
	secret, err := uaPriv.ECDH(asPub)
	if err != nil {
		return nil, err
	}
	cek, nonce, err := deriveKeys(secret, auth, salt, uaPriv.PublicKey().Bytes(), asPublic)
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(cek)
	if err != nil {
		return nil, err
	}
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil || len(pt) == 0 || pt[len(pt)-1] != 2 {
		return nil, errors.New("webpush: bad record")
	}
	return pt[:len(pt)-1], nil
}
