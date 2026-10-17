package evidence

import (
	"context"
	"crypto"
	"errors"
	"fmt"
	"time"

	"relic/internal/platform/database"
)

// TrustedKey is a public key that evidence signatures are checked against.
type TrustedKey struct {
	KID         string
	Alias       string
	Type        string
	PublicKey   string
	Fingerprint string
	IssuedBy    string
	Issued      time.Time
}

// AddTrustedKey stores a PEM public key under alias.
func (s *Service) AddTrustedKey(ctx context.Context, alias, pemText, user string) (TrustedKey, error) {
	if alias == "" {
		return TrustedKey{}, fmt.Errorf("%w: alias is required", ErrInvalid)
	}

	key, typ, err := ParsePublicKey(pemText)
	if err != nil {
		return TrustedKey{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}

	fp, err := Fingerprint(key)
	if err != nil {
		return TrustedKey{}, err
	}

	k := TrustedKey{
		KID:         fp[:16],
		Alias:       alias,
		Type:        typ,
		PublicKey:   pemText,
		Fingerprint: fp,
		IssuedBy:    user,
		Issued:      s.now(),
	}

	err = s.db.CreateTrustedKey(ctx, database.CreateTrustedKeyParams{
		Kid:         k.KID,
		Alias:       k.Alias,
		KeyType:     k.Type,
		PublicKey:   k.PublicKey,
		Fingerprint: k.Fingerprint,
		IssuedBy:    k.IssuedBy,
		Issued:      k.Issued,
	})
	if database.IsUniqueViolation(err) {
		return TrustedKey{}, ErrKeyExists
	}

	if err != nil {
		return TrustedKey{}, fmt.Errorf("store trusted key: %w", err)
	}

	return k, nil
}

// TrustedKeys lists the trusted keys by alias.
func (s *Service) TrustedKeys(ctx context.Context) ([]TrustedKey, error) {
	rows, err := s.db.ListTrustedKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("list trusted keys: %w", err)
	}

	out := make([]TrustedKey, 0, len(rows))
	for _, r := range rows {
		out = append(
			out,
			TrustedKey{
				KID:         r.Kid,
				Alias:       r.Alias,
				Type:        r.KeyType,
				PublicKey:   r.PublicKey,
				Fingerprint: r.Fingerprint,
				IssuedBy:    r.IssuedBy,
				Issued:      r.Issued,
			},
		)
	}

	return out, nil
}

// DeleteTrustedKey removes a key.
func (s *Service) DeleteTrustedKey(ctx context.Context, kid string) error {
	return s.db.WithTx(ctx, func(q database.Querier) error {
		if err := q.UnverifyEvidenceForKey(ctx, kid); err != nil {
			return fmt.Errorf("unverify evidence of trusted key: %w", err)
		}

		n, err := q.DeleteTrustedKey(ctx, kid)
		if err != nil {
			return fmt.Errorf("delete trusted key: %w", err)
		}

		if n == 0 {
			return ErrKeyNotFound
		}

		return nil
	})
}

func (s *Service) trustedKey(ctx context.Context, alias string) (crypto.PublicKey, error) {
	row, err := s.db.GetTrustedKeyByAlias(ctx, alias)
	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		return nil, fmt.Errorf("%w: key alias %s not found", ErrKeyNotFound, alias)
	}

	if err != nil {
		return nil, fmt.Errorf("get trusted key: %w", err)
	}

	key, _, err := ParsePublicKey(row.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("trusted key %s: %w", alias, err)
	}

	return key, nil
}
