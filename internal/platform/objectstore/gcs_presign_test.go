package objectstore

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestGCSPresignsGets checks the V4 signature of a presigned URL against the
// service account's public key, offline: emulators do not verify
// signatures.
func TestGCSPresignsGets(t *testing.T) { //nolint:paralleltest // sets the credentials environment variable
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}

	//nolint:gosec // a throwaway service account generated for the test
	account, err := json.Marshal(map[string]string{
		"type":           "service_account",
		"project_id":     "relic-test",
		"private_key_id": "k1",
		"private_key": string(pem.EncodeToMemory(&pem.Block{
			Type:  "PRIVATE KEY",
			Bytes: der,
		})),
		"client_email": "relic@relic-test.iam.gserviceaccount.com",
		"client_id":    "1",
		"token_uri":    "https://oauth2.googleapis.com/token",
	})
	if err != nil {
		t.Fatal(err)
	}

	file := filepath.Join(t.TempDir(), "account.json")
	if err := os.WriteFile(file, account, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", file)

	s, err := NewGCS(t.Context(), GCSConfig{
		ArtifactsBucket: "relic-blobs",
		TemporaryBucket: "relic-tmp",
	})
	if err != nil {
		t.Fatal(err)
	}

	raw, err := s.PresignGet(t.Context(), Key("ab"+strings.Repeat("1", 62)), time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}

	q := u.Query()
	expires, _ := strconv.Atoi(q.Get("X-Goog-Expires"))

	if u.Host != "storage.googleapis.com" || !strings.HasPrefix(u.Path, "/relic-blobs/sha256/ab/") ||
		q.Get("X-Goog-Algorithm") != "GOOG4-RSA-SHA256" || expires < 55 || expires > 60 ||
		!strings.HasPrefix(q.Get("X-Goog-Credential"), "relic@relic-test.iam.gserviceaccount.com/") {
		t.Fatalf("presigned URL = %s", raw)
	}

	if err := verifyV4(u, &key.PublicKey); err != nil {
		t.Errorf("signature of %s: %v", raw, err)
	}
}

// verifyV4 checks a V4 signed URL's signature as Cloud Storage does: over the
// canonical request of a GET with only the host header signed.
func verifyV4(u *url.URL, pub *rsa.PublicKey) error {
	q := u.Query()

	sig, err := hex.DecodeString(q.Get("X-Goog-Signature"))
	if err != nil {
		return err
	}

	q.Del("X-Goog-Signature")

	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}

	slices.Sort(keys)

	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, url.QueryEscape(k)+"="+strings.ReplaceAll(url.QueryEscape(q.Get(k)), "+", "%20"))
	}

	canonical := strings.Join([]string{
		"GET",
		u.EscapedPath(),
		strings.Join(pairs, "&"),
		"host:" + u.Host + "\n",
		"host",
		"UNSIGNED-PAYLOAD",
	}, "\n")
	requestHash := sha256.Sum256([]byte(canonical))

	_, scope, _ := strings.Cut(q.Get("X-Goog-Credential"), "/")
	toSign := strings.Join([]string{
		"GOOG4-RSA-SHA256",
		q.Get("X-Goog-Date"),
		scope,
		hex.EncodeToString(requestHash[:]),
	}, "\n")
	digest := sha256.Sum256([]byte(toSign))

	return rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig)
}
