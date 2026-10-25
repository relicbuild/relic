//go:build integration

package evidence

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"relic/internal/artifacts/repository"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore"
)

func TestMain(m *testing.M) { databasetest.Main(m) }

type evidenceFixture struct {
	evidence *Service
	repos    *repository.Service
	key      ed25519.PrivateKey
	kid      string
	subject  string
}

func newEvidenceFixture(t *testing.T) evidenceFixture {
	t.Helper()

	ctx := t.Context()
	db := databasetest.NewDB(t)

	blobs, err := objectstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	log := slog.New(slog.DiscardHandler)
	repos := repository.NewService(db, blobs, log)

	if err := repos.CreateRepository(ctx, repository.Repository{
		Name:   "libs-local",
		Class:  "local",
		Format: "generic",
	}); err != nil {
		t.Fatal(err)
	}

	item, err := repos.Deploy(ctx, repository.DeployInput{
		Repository: "libs-local",
		Path:       "dir/a.txt",
		Body:       strings.NewReader("first"),
		User:       "admin",
	})
	if err != nil {
		t.Fatal(err)
	}

	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	svc := New(db, repos, log)

	trusted, err := svc.AddTrustedKey(ctx, "ci", publicPEM(t, pub), "admin")
	if err != nil {
		t.Fatal(err)
	}

	return evidenceFixture{
		evidence: svc,
		repos:    repos,
		key:      key,
		kid:      trusted.KID,
		subject:  item.Checksums.SHA256,
	}
}

func (f evidenceFixture) envelope(
	t *testing.T,
	signatures func(payloadType string, payload []byte) []Signature,
) []byte {
	t.Helper()

	payload, err := json.Marshal(map[string]any{
		"_type": "https://in-toto.io/Statement/v1",
		"subject": []map[string]any{
			{
				"digest": map[string]string{
					"sha256": f.subject,
				},
			},
		},
		"predicateType": "https://relic.example.com/test",
		"predicate":     map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}

	const payloadType = "application/vnd.in-toto+json"

	body, err := json.Marshal(Envelope{
		Payload:     base64.StdEncoding.EncodeToString(payload),
		PayloadType: payloadType,
		Signatures:  signatures(payloadType, payload),
	})
	if err != nil {
		t.Fatal(err)
	}

	return body
}

func (f evidenceFixture) create(
	t *testing.T,
	signatures func(payloadType string, payload []byte) []Signature,
) Evidence {
	t.Helper()

	e, err := f.evidence.Create(t.Context(), Input{
		Repository: "libs-local",
		Path:       "dir/a.txt",
		Envelope:   f.envelope(t, signatures),
		User:       "admin",
	})
	if err != nil {
		t.Fatal(err)
	}

	return e
}

func (f evidenceFixture) signedByCI(t *testing.T) func(string, []byte) []Signature {
	t.Helper()

	return func(payloadType string, payload []byte) []Signature {
		return []Signature{
			{
				KeyID: "ci",
				Sig:   sign(t, f.key, payloadType, payload),
			},
		}
	}
}

func listed(t *testing.T, svc *Service) []Evidence {
	t.Helper()

	out, err := svc.ForSubject(t.Context(), "libs-local", "dir", "a.txt")
	if err != nil {
		t.Fatal(err)
	}

	return out
}

func TestCreateVerifiesAnyTrustedSignature(t *testing.T) {
	t.Parallel()

	f := newEvidenceFixture(t)
	e := f.create(t, func(payloadType string, payload []byte) []Signature {
		return append([]Signature{
			{
				KeyID: "stranger",
				Sig:   base64.StdEncoding.EncodeToString([]byte("not a signature")),
			},
		}, f.signedByCI(t)(payloadType, payload)...)
	})

	if !e.Verified || e.KeyAlias != "ci" {
		t.Errorf("evidence = verified %v by %q; want verified by ci", e.Verified, e.KeyAlias)
	}
}

func TestCreateRefusesEvidenceNoTrustedKeyVerifies(t *testing.T) {
	t.Parallel()

	f := newEvidenceFixture(t)

	_, strangerKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name       string
		signatures func(payloadType string, payload []byte) []Signature
		want       error
	}{
		{
			name: "a signature that names no key",
			signatures: func(payloadType string, payload []byte) []Signature {
				return []Signature{
					{
						Sig: sign(t, f.key, payloadType, payload),
					},
				}
			},
			want: ErrInvalid,
		},
		{
			name: "a trusted key's alias on a signature it did not make",
			signatures: func(payloadType string, payload []byte) []Signature {
				return []Signature{
					{
						KeyID: "ci",
						Sig:   sign(t, strangerKey, payloadType, payload),
					},
				}
			},
			want: ErrInvalid,
		},
		{
			name: "an alias no trusted key has",
			signatures: func(payloadType string, payload []byte) []Signature {
				return []Signature{
					{
						KeyID: "stranger",
						Sig:   sign(t, strangerKey, payloadType, payload),
					},
				}
			},
			want: ErrKeyNotFound,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.evidence.Create(t.Context(), Input{
				Repository: "libs-local",
				Path:       "dir/a.txt",
				Envelope:   f.envelope(t, tc.signatures),
				User:       "admin",
			})
			if !errors.Is(err, tc.want) {
				t.Errorf("Create = %v, want %v", err, tc.want)
			}
		})
	}

	if out := listed(t, f.evidence); len(out) != 0 {
		t.Errorf("refused evidence was stored: %+v", out)
	}
}

func TestEvidenceNeedsAFile(t *testing.T) {
	t.Parallel()

	f := newEvidenceFixture(t)
	f.subject = ""

	if _, err := f.evidence.Create(t.Context(), Input{
		Repository: "libs-local",
		Path:       "dir",
		Envelope:   f.envelope(t, f.signedByCI(t)),
		User:       "admin",
	}); !errors.Is(err, ErrInvalid) {
		t.Errorf("Create about a folder = %v, want ErrInvalid", err)
	}

	if _, err := f.evidence.Record(
		t.Context(),
		"libs-local",
		"dir",
		"https://relic.example.com/test",
		map[string]any{},
		"admin",
	); !errors.Is(err, ErrInvalid) {
		t.Errorf("Record about a folder = %v, want ErrInvalid", err)
	}
}

func TestDeletingAKeyUnverifiesItsEvidence(t *testing.T) {
	t.Parallel()

	f := newEvidenceFixture(t)
	if e := f.create(t, f.signedByCI(t)); !e.Verified {
		t.Fatal("evidence signed by a trusted key is not verified")
	}

	if err := f.evidence.DeleteTrustedKey(t.Context(), f.kid); err != nil {
		t.Fatal(err)
	}

	out := listed(t, f.evidence)
	if len(out) != 1 || out[0].Verified {
		t.Errorf("evidence after the key was deleted = %+v; want one unverified", out)
	}
}

func TestOverwrittenSubjectKeepsItsOldEvidenceOutOfItsCurrentContent(t *testing.T) {
	t.Parallel()

	f := newEvidenceFixture(t)
	f.create(t, f.signedByCI(t))

	if out := listed(t, f.evidence); len(out) != 1 {
		t.Fatalf("evidence before the overwrite = %+v; want one", out)
	}

	if _, err := f.repos.Deploy(t.Context(), repository.DeployInput{
		Repository: "libs-local",
		Path:       "dir/a.txt",
		Body:       strings.NewReader("second"),
		User:       "admin",
	}); err != nil {
		t.Fatal(err)
	}

	if out := listed(t, f.evidence); len(out) != 1 {
		t.Errorf("evidence after the overwrite = %+v; want the old one", out)
	}

	current, err := f.evidence.ForCurrentContent(t.Context(), "libs-local", "dir", "a.txt")
	if err != nil || len(current) != 0 {
		t.Errorf("evidence about the current content = %+v %v; want none", current, err)
	}
}
