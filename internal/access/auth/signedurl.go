package auth

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ErrBadSignature is returned for invalid or expired URL signatures.
var ErrBadSignature = errors.New("invalid or expired signature")

// SignPath signs a request path for user until ttl passes, for URLs a
// client fetches without its credentials (terraform module archives).
func (t *Tokens) SignPath(path, user string, ttl time.Duration) string {
	t.mu.RLock()
	kid, signer := t.kid, t.signer
	t.mu.RUnlock()
	exp := strconv.FormatInt(t.now().Add(ttl).Unix(), 10)
	sig := ed25519.Sign(signer, []byte(path+"\n"+user+"\n"+exp))

	return url.Values{
		"user": {
			user,
		},
		"exp": {
			exp,
		},
		"kid": {
			kid,
		},
		"sig": {
			base64.RawURLEncoding.EncodeToString(sig),
		},
	}.Encode()
}

// VerifyPath checks a SignPath signature and returns the signed user.
func (t *Tokens) VerifyPath(path, user, exp, kid, sig string) (string, error) {
	e, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || t.now().Unix() > e || strings.ContainsAny(user, "\n") {
		return "", ErrBadSignature
	}

	raw, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return "", ErrBadSignature
	}

	t.mu.RLock()
	key, ok := t.keys[kid]
	t.mu.RUnlock()

	if !ok || !ed25519.Verify(key, []byte(path+"\n"+user+"\n"+exp), raw) {
		return "", ErrBadSignature
	}

	return user, nil
}
