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
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"strconv"
)

// Envelope is a DSSE envelope.
type Envelope struct {
	Payload     string      `json:"payload"`
	PayloadType string      `json:"payloadType"`
	Signatures  []Signature `json:"signatures"`
}

// Signature is one DSSE signature; KeyID is the signing key's alias.
type Signature struct {
	KeyID string `json:"keyid"`
	Sig   string `json:"sig"`
}

// AttachmentPlace says where an attached file, named by its SHA-256, is
// stored.
type AttachmentPlace struct {
	Repository string
	Path       string
	SHA256     string
}

func pae(payloadType string, payload []byte) []byte {
	out := []byte(
		"DSSEv1 " + strconv.Itoa(len(payloadType)) + " " + payloadType + " " + strconv.Itoa(len(payload)) + " ",
	)

	return append(out, payload...)
}

// ParsePublicKey parses a PEM (PKIX, or PKCS#1 for RSA) public key and
// returns it with its type: "ecdsa", "rsa" or "ed25519".
func ParsePublicKey(pemText string) (crypto.PublicKey, string, error) {
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		return nil, "", errors.New("no PEM block")
	}

	var (
		key crypto.PublicKey
		err error
	)
	if block.Type == "RSA PUBLIC KEY" {
		key, err = x509.ParsePKCS1PublicKey(block.Bytes)
	} else {
		key, err = x509.ParsePKIXPublicKey(block.Bytes)
	}

	if err != nil {
		return nil, "", fmt.Errorf("parse public key: %w", err)
	}

	switch key.(type) {
	case *ecdsa.PublicKey:
		return key, "ecdsa", nil
	case *rsa.PublicKey:
		return key, "rsa", nil
	case ed25519.PublicKey:
		return key, "ed25519", nil
	}

	return nil, "", fmt.Errorf("unsupported public key type %T", key)
}

// Fingerprint is the hex SHA-256 of the key's DER encoding.
func Fingerprint(key crypto.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		return "", fmt.Errorf("encode public key: %w", err)
	}

	sum := sha256.Sum256(der)

	return hex.EncodeToString(sum[:]), nil
}

func verifySignature(key crypto.PublicKey, payloadType string, payload []byte, sig string) bool {
	raw, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		return false
	}

	msg := pae(payloadType, payload)

	switch k := key.(type) {
	case *ecdsa.PublicKey:
		var digest []byte

		switch k.Curve {
		case elliptic.P384():
			d := sha512.Sum384(msg)
			digest = d[:]
		case elliptic.P521():
			d := sha512.Sum512(msg)
			digest = d[:]
		default:
			d := sha256.Sum256(msg)
			digest = d[:]
		}

		return ecdsa.VerifyASN1(k, digest, raw)
	case *rsa.PublicKey:
		d := sha256.Sum256(msg)
		return rsa.VerifyPKCS1v15(k, crypto.SHA256, d[:], raw) == nil
	case ed25519.PublicKey:
		return ed25519.Verify(k, msg, raw)
	}

	return false
}

// StatementType is the DSSE payload type of an in-toto statement.
const StatementType = "application/vnd.in-toto+json"

// Sign signs payload into a DSSE envelope with the private key, whose alias
// becomes the signature's key ID.
func Sign(key crypto.Signer, alias, payloadType string, payload []byte) (Envelope, error) {
	msg := pae(payloadType, payload)

	var (
		sig []byte
		err error
	)

	switch k := key.(type) {
	case ed25519.PrivateKey:
		sig = ed25519.Sign(k, msg)
	case *ecdsa.PrivateKey:
		var digest []byte

		switch k.Curve {
		case elliptic.P384():
			d := sha512.Sum384(msg)
			digest = d[:]
		case elliptic.P521():
			d := sha512.Sum512(msg)
			digest = d[:]
		default:
			d := sha256.Sum256(msg)
			digest = d[:]
		}

		sig, err = ecdsa.SignASN1(rand.Reader, k, digest)
	case *rsa.PrivateKey:
		d := sha256.Sum256(msg)
		sig, err = rsa.SignPKCS1v15(rand.Reader, k, crypto.SHA256, d[:])
	default:
		return Envelope{}, fmt.Errorf("unsupported private key type %T", key)
	}

	if err != nil {
		return Envelope{}, fmt.Errorf("sign: %w", err)
	}

	return Envelope{
		Payload:     base64.StdEncoding.EncodeToString(payload),
		PayloadType: payloadType,
		Signatures: []Signature{
			{
				KeyID: alias,
				Sig:   base64.StdEncoding.EncodeToString(sig),
			},
		},
	}, nil
}

// Verify reports whether one of the envelope's signatures verifies with the
// key.
func Verify(key crypto.PublicKey, env Envelope) bool {
	payload, err := base64.StdEncoding.DecodeString(env.Payload)
	if err != nil {
		return false
	}

	for _, s := range env.Signatures {
		if verifySignature(key, env.PayloadType, payload, s.Sig) {
			return true
		}
	}

	return false
}

// ParsePrivateKey parses a PEM private key (PKCS#8, or PKCS#1 for RSA, or
// SEC 1 for ECDSA).
func ParsePrivateKey(pemText []byte) (crypto.Signer, error) {
	block, _ := pem.Decode(pemText)
	if block == nil {
		return nil, errors.New("no PEM block")
	}

	var (
		key any
		err error
	)

	switch block.Type {
	case "RSA PRIVATE KEY":
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "EC PRIVATE KEY":
		key, err = x509.ParseECPrivateKey(block.Bytes)
	default:
		key, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	}

	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}

	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("unsupported private key type %T", key)
	}

	return signer, nil
}
