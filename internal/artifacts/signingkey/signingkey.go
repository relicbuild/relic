// Package signingkey keeps the key pairs that sign repository metadata: RSA for
// Alpine indexes, OpenPGP for Debian Release files.
package signingkey

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"time"

	"relic/internal/platform/database"
	"relic/internal/platform/sealing"
)

// Default key pair names, used by repositories without a primaryKeyPairRef.
const (
	DefaultRSA = "relic-rsa"
	DefaultGPG = "relic-gpg"
)

// ErrKind is returned for a key pair of the other kind.
var ErrKind = errors.New("key pair is of another kind")

// ErrNotFound is returned for a key pair that does not exist.
var ErrNotFound = errors.New("key pair not found")

// Service reads and creates key pairs.
type Service struct {
	db  *database.DB
	box *sealing.Box
}

// New returns a Service.
func New(db *database.DB, box *sealing.Box) *Service {
	return &Service{
		db:  db,
		box: box,
	}
}

func place(name string) string { return "key_pairs/" + name }

// SealStored seals private keys stored before RELIC_ENCRYPTION_KEY was set.
func (s *Service) SealStored(ctx context.Context) error {
	if !s.box.Enabled() {
		return nil
	}

	rows, err := s.db.ListKeyPairs(ctx)
	if err != nil {
		return fmt.Errorf("list key pairs: %w", err)
	}

	for _, r := range rows {
		if sealing.Sealed(r.PrivateKey) {
			continue
		}

		sealed, err := s.box.Seal(r.PrivateKey, place(r.Name))
		if err != nil {
			return fmt.Errorf("seal the private key of %s: %w", r.Name, err)
		}

		if err := s.db.UpdateKeyPairPrivateKey(
			ctx,
			database.UpdateKeyPairPrivateKeyParams{
				Name:       r.Name,
				PrivateKey: sealed,
			},
		); err != nil {
			return fmt.Errorf("seal key pair %s: %w", r.Name, err)
		}
	}

	return nil
}

const rsaBits = 3072

// RSA returns the named RSA key pair, creating it on first use, with its
// public key as PEM ("BEGIN PUBLIC KEY").
func (s *Service) RSA(ctx context.Context, name string) (*rsa.PrivateKey, string, error) {
	row, err := s.getOrCreate(ctx, name, "rsa", func() ([]byte, string, error) {
		key, err := rsa.GenerateKey(rand.Reader, rsaBits)
		if err != nil {
			return nil, "", err
		}

		pub, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
		if err != nil {
			return nil, "", err
		}

		return x509.MarshalPKCS1PrivateKey(key), string(
			pem.EncodeToMemory(&pem.Block{
				Type:  "PUBLIC KEY",
				Bytes: pub,
			}),
		), nil
	})
	if err != nil {
		return nil, "", err
	}

	key, err := x509.ParsePKCS1PrivateKey(row.PrivateKey)
	if err != nil {
		return nil, "", fmt.Errorf("key pair %s: %w", name, err)
	}

	return key, row.PublicKey, nil
}

// PublicKey returns a key pair's public key, if it exists.
func (s *Service) PublicKey(ctx context.Context, name string) (string, error) {
	row, err := s.db.GetKeyPair(ctx, name)
	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		return "", fmt.Errorf("%w: %s", ErrNotFound, name)
	}

	if err != nil {
		return "", fmt.Errorf("key pair %s: %w", name, err)
	}

	return row.PublicKey, nil
}

// KeyPair is a stored key pair without its private key.
type KeyPair struct {
	Name      string
	Kind      string
	PublicKey string
	Created   time.Time
}

// List returns the stored key pairs, by name, without their private keys.
func (s *Service) List(ctx context.Context) ([]KeyPair, error) {
	rows, err := s.db.ListKeyPairs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list key pairs: %w", err)
	}

	out := make([]KeyPair, 0, len(rows))
	for _, r := range rows {
		out = append(out, KeyPair{
			Name:      r.Name,
			Kind:      r.Kind,
			PublicKey: r.PublicKey,
			Created:   r.Created,
		})
	}

	return out, nil
}

func (s *Service) getOrCreate(
	ctx context.Context,
	name, kind string,
	generate func() ([]byte, string, error),
) (database.KeyPair, error) {
	row, err := s.db.GetKeyPair(ctx, name)
	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		priv, pub, gerr := generate()
		if gerr != nil {
			return database.KeyPair{}, fmt.Errorf("generate key pair %s: %w", name, gerr)
		}

		sealed, serr := s.box.Seal(priv, place(name))
		if serr != nil {
			return database.KeyPair{}, serr
		}

		if err := s.db.InsertKeyPair(
			ctx,
			database.InsertKeyPairParams{
				Name:       name,
				Kind:       kind,
				PrivateKey: sealed,
				PublicKey:  pub,
			},
		); err != nil {
			return database.KeyPair{}, fmt.Errorf("store key pair %s: %w", name, err)
		}

		row, err = s.db.GetKeyPair(ctx, name)
	}

	if err != nil {
		return database.KeyPair{}, fmt.Errorf("key pair %s: %w", name, err)
	}

	if row.Kind != kind {
		return database.KeyPair{}, fmt.Errorf("%w: %s is %s", ErrKind, name, row.Kind)
	}

	if row.PrivateKey, err = s.box.Open(row.PrivateKey, place(name)); err != nil {
		return database.KeyPair{}, fmt.Errorf("key pair %s: %w", name, err)
	}

	return row, nil
}
