package evidence

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"testing"
)

func sign(t *testing.T, key crypto.Signer, payloadType string, payload []byte) string {
	t.Helper()

	msg := pae(payloadType, payload)

	var (
		sig []byte
		err error
	)

	switch k := key.(type) {
	case *ecdsa.PrivateKey:
		var digest []byte

		if k.Curve == elliptic.P384() {
			d := sha512.Sum384(msg)
			digest = d[:]
		} else {
			d := sha256.Sum256(msg)
			digest = d[:]
		}

		sig, err = ecdsa.SignASN1(rand.Reader, k, digest)
	case *rsa.PrivateKey:
		d := sha256.Sum256(msg)
		sig, err = rsa.SignPKCS1v15(rand.Reader, k, crypto.SHA256, d[:])
	case ed25519.PrivateKey:
		sig = ed25519.Sign(k, msg)
	}

	if err != nil {
		t.Fatal(err)
	}

	return base64.StdEncoding.EncodeToString(sig)
}

func publicPEM(t *testing.T, key crypto.PublicKey) string {
	t.Helper()

	der, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		t.Fatal(err)
	}

	return string(pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: der,
	}))
}

func TestVerifySignature(t *testing.T) {
	t.Parallel()

	p256, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	_, edKey, _ := ed25519.GenerateKey(rand.Reader)
	tests := []struct {
		name     string
		key      crypto.Signer
		wantType string
	}{
		{
			name:     "ecdsa p256",
			key:      p256,
			wantType: "ecdsa",
		},
		{
			name:     "ecdsa p384",
			key:      p384,
			wantType: "ecdsa",
		},
		{
			name:     "rsa pkcs1v15",
			key:      rsaKey,
			wantType: "rsa",
		},
		{
			name:     "ed25519",
			key:      edKey,
			wantType: "ed25519",
		},
	}

	const payloadType = "application/vnd.in-toto+json"

	payload := []byte(`{"_type":"https://in-toto.io/Statement/v1"}`)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pub, typ, err := ParsePublicKey(publicPEM(t, tt.key.Public()))
			if err != nil || typ != tt.wantType {
				t.Fatalf("ParsePublicKey = %s, %v", typ, err)
			}

			sig := sign(t, tt.key, payloadType, payload)
			if !verifySignature(pub, payloadType, payload, sig) {
				t.Error("valid signature rejected")
			}

			if verifySignature(pub, payloadType, append([]byte{
				' ',
			}, payload...), sig) {
				t.Error("signature over other bytes accepted")
			}

			if verifySignature(pub, payloadType, payload, "not base64!") {
				t.Error("garbage signature accepted")
			}
		})
	}
}

func TestPAE(t *testing.T) {
	t.Parallel()

	if got := string(pae("t", []byte("hello"))); got != "DSSEv1 1 t 5 hello" {
		t.Errorf("pae = %q", got)
	}
}

func TestSlug(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in, want string
	}{
		{
			"https://slsa.dev/provenance/v1",
			"provenance",
		},
		{
			"https://in-toto.io/attestation/test-result/v0.1",
			"test-result",
		},
		{
			"https://cyclonedx.org/bom",
			"bom",
		},
		{
			"custom",
			"custom",
		},
		{
			"",
			"evidence",
		},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()

			if got := slug(tt.in); got != tt.want {
				t.Errorf("slug(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSignVerifies(t *testing.T) {
	t.Parallel()

	ecKey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	_, edKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	for name, key := range map[string]crypto.Signer{
		"ecdsa P-384": ecKey,
		"rsa":         rsaKey,
		"ed25519":     edKey,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			der, err := x509.MarshalPKCS8PrivateKey(key)
			if err != nil {
				t.Fatal(err)
			}

			parsed, err := ParsePrivateKey(pem.EncodeToMemory(&pem.Block{
				Type:  "PRIVATE KEY",
				Bytes: der,
			}))
			if err != nil {
				t.Fatal(err)
			}

			env, err := Sign(parsed, "ci", StatementType, []byte(`{"_type":"https://in-toto.io/Statement/v1"}`))
			if err != nil {
				t.Fatal(err)
			}

			if env.Signatures[0].KeyID != "ci" || !Verify(key.Public(), env) {
				t.Errorf("envelope %+v does not verify", env)
			}

			env.Payload = base64.StdEncoding.EncodeToString([]byte(`{"_type":"tampered"}`))
			if Verify(key.Public(), env) {
				t.Error("a tampered payload verified")
			}
		})
	}
}
