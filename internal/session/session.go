// Package session implements Steward's stateless session: a JSON payload
// sealed with AES-256-GCM (key derived from Config.SecretKey) carried in an
// HttpOnly cookie. Tampering or a key change invalidates the session; there
// is no server-side store to clean up.
package session

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"
)

// Flash is a one-shot message rendered on the next page view.
type Flash struct {
	Type    string `json:"t"` // success | error | warning | info
	Message string `json:"m"`
}

// Data is the decrypted session payload.
type Data struct {
	UID      uint    `json:"uid,omitempty"`
	CSRF     string  `json:"csrf,omitempty"`
	Flashes  []Flash `json:"f,omitempty"`
	IssuedAt int64   `json:"iat"`
}

// MaxAge bounds session validity independent of cookie expiry.
const MaxAge = 30 * 24 * time.Hour

// ErrInvalid covers any undecodable, tampered, or expired session value.
var ErrInvalid = errors.New("session: invalid")

// Codec seals and opens session payloads.
type Codec struct {
	aead cipher.AEAD
}

// NewCodec derives an AES-256-GCM key from the secret via SHA-256.
func NewCodec(secret []byte) (*Codec, error) {
	if len(secret) == 0 {
		return nil, errors.New("session: empty secret")
	}
	key := sha256.Sum256(secret)
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Codec{aead: aead}, nil
}

// Encode seals the payload into a cookie-safe string.
func (c *Codec) Encode(d *Data) (string, error) {
	if d.IssuedAt == 0 {
		d.IssuedAt = time.Now().Unix()
	}
	plain, err := json.Marshal(d)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := c.aead.Seal(nonce, nonce, plain, nil)
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

// Decode opens a cookie value; any failure returns ErrInvalid.
func (c *Codec) Decode(value string) (*Data, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) < c.aead.NonceSize() {
		return nil, ErrInvalid
	}
	nonce, sealed := raw[:c.aead.NonceSize()], raw[c.aead.NonceSize():]
	plain, err := c.aead.Open(nil, nonce, sealed, nil)
	if err != nil {
		return nil, ErrInvalid
	}
	var d Data
	if err := json.Unmarshal(plain, &d); err != nil {
		return nil, ErrInvalid
	}
	if d.IssuedAt == 0 || time.Since(time.Unix(d.IssuedAt, 0)) > MaxAge {
		return nil, ErrInvalid
	}
	return &d, nil
}

// NewToken returns a random URL-safe token (CSRF, remember-me).
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
