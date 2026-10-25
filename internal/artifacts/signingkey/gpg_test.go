package signingkey

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

func TestGPGRoundTrip(t *testing.T) {
	t.Parallel()

	e, err := openpgp.NewEntity("t", "", "", &packet.Config{
		Algorithm: packet.PubKeyAlgoRSA,
		RSABits:   2048,
	})
	if err != nil {
		t.Fatal(err)
	}

	var priv bytes.Buffer
	if err := e.SerializePrivate(&priv, nil); err != nil {
		t.Fatal(err)
	}

	back, err := openpgp.ReadEntity(packet.NewReader(&priv))
	if err != nil || back.PrimaryKey.KeyId != e.PrimaryKey.KeyId || back.PrivateKey == nil {
		t.Fatalf("round trip = %v %v", back, err)
	}

	pub, err := ArmoredPublicKeys(e)
	if err != nil || !strings.HasPrefix(pub, "-----BEGIN PGP PUBLIC KEY BLOCK-----") {
		t.Fatalf("armored public key = %q %v", pub, err)
	}

	ring, err := openpgp.ReadArmoredKeyRing(strings.NewReader(pub))
	if err != nil || len(ring) != 1 || ring[0].PrivateKey != nil {
		t.Errorf("public key ring = %v %v", ring, err)
	}
}

func TestSignaturesOfSeveralKeys(t *testing.T) {
	t.Parallel()

	keys := make([]*openpgp.Entity, 0, 2)

	for range 2 {
		e, err := openpgp.NewEntity("t", "", "", &packet.Config{
			Algorithm: packet.PubKeyAlgoRSA,
			RSABits:   2048,
		})
		if err != nil {
			t.Fatal(err)
		}

		keys = append(keys, e)
	}

	data := []byte("Origin: relic\nSuite: bookworm\n")

	detached, err := DetachSign(data, keys...)
	if err != nil {
		t.Fatal(err)
	}

	clearSigned, err := ClearSign(data, keys...)
	if err != nil {
		t.Fatal(err)
	}

	both, err := ArmoredPublicKeys(keys...)
	if err != nil {
		t.Fatal(err)
	}

	ring, err := openpgp.ReadArmoredKeyRing(strings.NewReader(both))
	if err != nil || len(ring) != 2 {
		t.Fatalf("the public keys of two key pairs read back as %d keys, %v", len(ring), err)
	}

	for i, key := range keys {
		trusted := openpgp.EntityList{
			key,
		}

		if _, err := openpgp.CheckArmoredDetachedSignature(
			trusted,
			bytes.NewReader(data),
			bytes.NewReader(detached),
			nil,
		); err != nil {
			t.Errorf("the detached signatures do not verify with key %d alone: %v", i, err)
		}

		block, _ := clearsign.Decode(clearSigned)
		if block == nil {
			t.Fatal("the clear-signed text does not decode")
		}

		if _, err := block.VerifySignature(trusted, nil); err != nil {
			t.Errorf("the clear-signed text does not verify with key %d alone: %v", i, err)
		}
	}
}
