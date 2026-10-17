package auth

import (
	"strings"
	"testing"
)

func TestHashAndVerifyPassword(t *testing.T) {
	t.Parallel()

	hash, err := HashPassword("s3cret")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(hash, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("unexpected hash format %q", hash)
	}

	tests := []struct {
		name     string
		password string
		want     bool
	}{
		{
			name:     "correct",
			password: "s3cret",
			want:     true,
		},
		{
			name:     "wrong",
			password: "s3cret!",
			want:     false,
		},
		{
			name:     "empty",
			password: "",
			want:     false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := VerifyPassword(tt.password, hash)
			if err != nil || got != tt.want {
				t.Errorf("VerifyPassword(%q) = %v, %v; want %v", tt.password, got, err, tt.want)
			}
		})
	}
}

func TestHashPasswordUsesFreshSalt(t *testing.T) {
	t.Parallel()

	a, _ := HashPassword("x")

	b, _ := HashPassword("x")
	if a == b {
		t.Error("two hashes of the same password are identical")
	}
}

func TestVerifyPasswordRejectsMalformedHashes(t *testing.T) {
	t.Parallel()

	for _, hash := range []string{
		"",
		"plain",
		"$argon2i$v=19$m=1,t=1,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=18$m=1,t=1,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=x$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=1,t=1,p=1$!!$aGFzaA",
		"$argon2id$v=19$m=1,t=1,p=1$c2FsdA$!!",
	} {
		t.Run(hash, func(t *testing.T) {
			t.Parallel()

			if _, err := VerifyPassword("x", hash); err == nil {
				t.Errorf("VerifyPassword accepted %q", hash)
			}
		})
	}
}
