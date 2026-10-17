//go:build integration

package signingkey

import (
	"errors"
	"testing"

	"relic/internal/platform/database/databasetest"
)

func TestMain(m *testing.M) { databasetest.Main(m) }

func TestPublicKey(t *testing.T) {
	t.Parallel()

	s := New(databasetest.NewDB(t), nil)
	if _, err := s.PublicKey(t.Context(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown key pair: err = %v, want ErrNotFound", err)
	}

	_, pem, err := s.RSA(t.Context(), "signing")
	if err != nil {
		t.Fatal(err)
	}

	if got, err := s.PublicKey(t.Context(), "signing"); err != nil || got != pem {
		t.Errorf("public key = %q %v, want the created one", got, err)
	}
}
