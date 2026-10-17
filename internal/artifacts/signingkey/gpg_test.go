package signingkey

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
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

	pub, err := armoredPublic(e)
	if err != nil || !strings.HasPrefix(pub, "-----BEGIN PGP PUBLIC KEY BLOCK-----") {
		t.Fatalf("armored public key = %q %v", pub, err)
	}

	ring, err := openpgp.ReadArmoredKeyRing(strings.NewReader(pub))
	if err != nil || len(ring) != 1 || ring[0].PrivateKey != nil {
		t.Errorf("public key ring = %v %v", ring, err)
	}
}
