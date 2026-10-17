package main

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spf13/cobra"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/delivery/evidence"
)

func TestVerifyEvidenceWithAnySigner(t *testing.T) {
	t.Parallel()

	payload := []byte(
		`{"_type":"https://in-toto.io/Statement/v1","subject":[{"name":"app.jar","digest":{"sha256":"abc"}}]}`,
	)

	var envelope evidence.Envelope

	var trustedPublic crypto.PublicKey

	for _, alias := range []string{
		"other",
		"ci",
	} {
		public, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}

		env, err := evidence.Sign(private, alias, "application/vnd.in-toto+json", payload)
		if err != nil {
			t.Fatal(err)
		}

		envelope.Payload, envelope.PayloadType = env.Payload, env.PayloadType
		envelope.Signatures = append(envelope.Signatures, env.Signatures...)

		if alias == "ci" {
			trustedPublic = public
		}
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(envelope)
	}))
	t.Cleanup(srv.Close)

	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())

	got := verifyEvidence(
		cmd,
		&client{
			url:  srv.URL,
			http: srv.Client(),
		},
		&relicv1.Evidence{
			StoredPath: "app.jar/envelope.json",
		},
		map[string][]crypto.PublicKey{
			"ci": {
				trustedPublic,
			},
		},
		"abc",
	)
	if got != "" {
		t.Errorf("verifyEvidence = %q, want verified by the second signature", got)
	}
}
