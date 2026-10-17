package sso

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"
)

const (
	keyBits     = 3072
	keyLifetime = 10 * 365 * 24 * time.Hour
)

// newKeyPair returns a PEM RSA key and a self-signed certificate naming the
// provider, for signing SAML requests and decrypting assertions.
func newKeyPair(name string, now time.Time) (string, string, error) {
	key, err := rsa.GenerateKey(rand.Reader, keyBits)
	if err != nil {
		return "", "", fmt.Errorf("generate key for %s: %w", name, err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return "", "", fmt.Errorf("serial number for %s: %w", name, err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: "relic sso " + name,
		},
		NotBefore: now.Add(-time.Hour),
		NotAfter:  now.Add(keyLifetime),
		KeyUsage:  x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return "", "", fmt.Errorf("create certificate for %s: %w", name, err)
	}

	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", "", fmt.Errorf("encode key for %s: %w", name, err)
	}

	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: pkcs8,
	})
	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: der,
	})

	return string(keyPEM), string(certPEM), nil
}
