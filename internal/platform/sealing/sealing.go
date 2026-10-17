// Package sealing seals secrets stored in the database.
package sealing

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

const magic = "rs1\x00"

const stringPrefix = "{relic-sealed}"

// ErrNoKey is returned when a sealed value is read without a key.
var ErrNoKey = errors.New("secret is sealed but RELIC_ENCRYPTION_KEY is not set")

// ErrOpen is returned when a sealed value does not open.
var ErrOpen = errors.New("secret does not open with RELIC_ENCRYPTION_KEY")

// Box seals and opens secrets.
type Box struct {
	aead cipher.AEAD
}

// ParseKey decodes RELIC_ENCRYPTION_KEY: 32 bytes in standard or URL-safe
// base64.
func ParseKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		if key, err := enc.DecodeString(s); err == nil && len(key) == 32 {
			return key, nil
		}
	}

	return nil, errors.New("RELIC_ENCRYPTION_KEY must be 32 bytes in base64 (openssl rand -base64 32)")
}

// New returns a Box sealing with key (32 bytes), or a pass-through Box
// when key is empty.
func New(key []byte) (*Box, error) {
	if len(key) == 0 {
		return &Box{}, nil
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secrets key: %w", err)
	}

	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secrets key: %w", err)
	}

	return &Box{
		aead: aead,
	}, nil
}

// Enabled reports whether values are sealed.
func (b *Box) Enabled() bool { return b != nil && b.aead != nil }

// Sealed reports whether a stored value is sealed.
func Sealed(stored []byte) bool { return strings.HasPrefix(string(stored), magic) }

// Seal seals plain for storage at where (e.g. "signing_keys/<kid>").
func (b *Box) Seal(plain []byte, where string) ([]byte, error) {
	if !b.Enabled() {
		return plain, nil
	}

	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("seal: %w", err)
	}

	out := append([]byte(magic), nonce...)

	return b.aead.Seal(out, nonce, plain, []byte(where)), nil
}

// Open opens a value stored at where.
func (b *Box) Open(stored []byte, where string) ([]byte, error) {
	if !Sealed(stored) {
		return stored, nil
	}

	if !b.Enabled() {
		return nil, ErrNoKey
	}

	rest := stored[len(magic):]

	n := b.aead.NonceSize()
	if len(rest) < n {
		return nil, ErrOpen
	}

	plain, err := b.aead.Open(nil, rest[:n], rest[n:], []byte(where))
	if err != nil {
		return nil, ErrOpen
	}

	return plain, nil
}

// SealString seals a string value, e.g. a password in a JSON document.
func (b *Box) SealString(plain, where string) (string, error) {
	if plain == "" || !b.Enabled() || strings.HasPrefix(plain, stringPrefix) {
		return plain, nil
	}

	sealed, err := b.Seal([]byte(plain), where)
	if err != nil {
		return "", err
	}

	return stringPrefix + base64.RawStdEncoding.EncodeToString(sealed[len(magic):]), nil
}

// OpenString opens a value from SealString; other strings are returned as
// they are.
func (b *Box) OpenString(stored, where string) (string, error) {
	enc, ok := strings.CutPrefix(stored, stringPrefix)
	if !ok {
		return stored, nil
	}

	raw, err := base64.RawStdEncoding.DecodeString(enc)
	if err != nil {
		return "", ErrOpen
	}

	plain, err := b.Open(append([]byte(magic), raw...), where)

	return string(plain), err
}

// SealedString reports whether a string value is sealed.
func SealedString(stored string) bool { return strings.HasPrefix(stored, stringPrefix) }

// ContainsSealedString reports whether doc (e.g. a JSON document) may hold
// a sealed string.
func ContainsSealedString(doc []byte) bool { return bytes.Contains(doc, []byte(stringPrefix)) }
