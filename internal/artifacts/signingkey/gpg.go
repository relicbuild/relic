package signingkey

import (
	"bytes"
	"context"
	"fmt"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

// GPG returns the named OpenPGP key pair, creating it on first use, with
// its armored public key.
func (s *Service) GPG(ctx context.Context, name string) (*openpgp.Entity, string, error) {
	row, err := s.getOrCreate(ctx, name, "gpg", func() ([]byte, string, error) {
		e, err := openpgp.NewEntity(
			"Relic repository signing",
			name,
			"",
			&packet.Config{
				Algorithm: packet.PubKeyAlgoRSA,
				RSABits:   rsaBits,
			},
		)
		if err != nil {
			return nil, "", err
		}

		var priv bytes.Buffer
		if err := e.SerializePrivate(&priv, nil); err != nil {
			return nil, "", err
		}

		pub, err := ArmoredPublicKeys(e)
		if err != nil {
			return nil, "", err
		}

		return priv.Bytes(), pub, nil
	})
	if err != nil {
		return nil, "", err
	}

	e, err := openpgp.ReadEntity(packet.NewReader(bytes.NewReader(row.PrivateKey)))
	if err != nil {
		return nil, "", fmt.Errorf("key pair %s: %w", name, err)
	}

	return e, row.PublicKey, nil
}

// GPGKeys returns the named OpenPGP key pairs, creating them on first use,
// with their public keys in one armored block.
func (s *Service) GPGKeys(ctx context.Context, names ...string) ([]*openpgp.Entity, string, error) {
	keys := make([]*openpgp.Entity, 0, len(names))

	for _, name := range names {
		key, _, err := s.GPG(ctx, name)
		if err != nil {
			return nil, "", err
		}

		keys = append(keys, key)
	}

	public, err := ArmoredPublicKeys(keys...)
	if err != nil {
		return nil, "", err
	}

	return keys, public, nil
}

// ArmoredPublicKeys returns the public keys of keys in one armored block.
func ArmoredPublicKeys(keys ...*openpgp.Entity) (string, error) {
	var buf bytes.Buffer

	w, err := armor.Encode(&buf, openpgp.PublicKeyType, nil)
	if err != nil {
		return "", err
	}

	for _, e := range keys {
		if err := e.Serialize(w); err != nil {
			return "", err
		}
	}

	if err := w.Close(); err != nil {
		return "", err
	}

	buf.WriteString("\n")

	return buf.String(), nil
}

// ClearSign returns data clear-signed with each of keys, as Debian's
// InRelease.
func ClearSign(data []byte, keys ...*openpgp.Entity) ([]byte, error) {
	privateKeys := make([]*packet.PrivateKey, 0, len(keys))
	for _, k := range keys {
		privateKeys = append(privateKeys, k.PrivateKey)
	}

	var buf bytes.Buffer

	w, err := clearsign.EncodeMulti(&buf, privateKeys, nil)
	if err != nil {
		return nil, err
	}

	if _, err := w.Write(data); err != nil {
		return nil, err
	}

	if err := w.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// DetachSign returns an armored block of detached signatures of data, one by
// each of keys, as Debian's Release.gpg and RPM's repomd.xml.asc.
func DetachSign(data []byte, keys ...*openpgp.Entity) ([]byte, error) {
	var buf bytes.Buffer

	w, err := armor.Encode(&buf, openpgp.SignatureType, nil)
	if err != nil {
		return nil, err
	}

	for _, k := range keys {
		if err := openpgp.DetachSign(w, k, bytes.NewReader(data), nil); err != nil {
			return nil, err
		}
	}

	if err := w.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}
