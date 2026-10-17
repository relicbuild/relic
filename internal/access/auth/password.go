// Package auth authenticates requests and authorizes them against the access
// level each route declares.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

const (
	argonMemory  = 19 * 1024
	argonTime    = 2
	argonThreads = 1
	argonKeyLen  = 32
	saltLen      = 16
)

var errBadHash = errors.New("malformed password hash")

// argonSlots bounds concurrent verifications.
var argonSlots = make(chan struct{}, 2*runtime.GOMAXPROCS(0))

// dummyHash is verified for unknown users, so a failed login costs the same
// whether or not the user exists.
var dummyHash = sync.OnceValue(func() string {
	h, err := HashPassword("relic-dummy-password")
	if err != nil {
		panic(err)
	}

	return h
})

// HashPassword returns an argon2id hash in PHC string format.
func HashPassword(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("salt: %w", err)
	}

	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword reports whether password matches a hash from HashPassword,
// honouring the parameters recorded in the hash.
func VerifyPassword(password, hash string) (bool, error) {
	argonSlots <- struct{}{}
	defer func() {
		<-argonSlots
	}()

	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errBadHash
	}

	var (
		version            int
		memory, iterations uint32
		threads            uint8
	)

	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errBadHash
	}

	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &threads); err != nil {
		return false, errBadHash
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, errBadHash
	}

	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, errBadHash
	}

	//nolint:gosec // len of a decoded 32-byte key
	got := argon2.IDKey([]byte(password), salt, iterations, memory, threads, uint32(len(want)))

	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
