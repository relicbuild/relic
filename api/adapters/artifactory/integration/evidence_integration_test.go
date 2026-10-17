//go:build integration

package integration

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func envelope(t *testing.T, key *ecdsa.PrivateKey, alias, subjectSHA256 string) string {
	t.Helper()

	statement, _ := json.Marshal(map[string]any{
		"_type": "https://in-toto.io/Statement/v1",
		"subject": []any{
			map[string]any{
				"digest": map[string]string{
					"sha256": subjectSHA256,
				},
			},
		},
		"predicateType": "https://slsa.dev/provenance/v1",
		"predicate": map[string]string{
			"builder": "ci",
		},
		"createdAt": "2026-09-24T10:11:12.345Z",
		"createdBy": "admin",
	})

	const payloadType = "application/vnd.in-toto+json"

	pae := "DSSEv1 " + strconv.Itoa(len(payloadType)) + " " + payloadType + " " + strconv.Itoa(
		len(statement),
	) + " " + string(statement)
	digest := sha256.Sum256([]byte(pae))

	sig, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}

	env, _ := json.Marshal(map[string]any{
		"payload":     base64.StdEncoding.EncodeToString(statement),
		"payloadType": payloadType,
		"signatures": []any{
			map[string]string{
				"keyid": alias,
				"sig":   base64.StdEncoding.EncodeToString(sig),
			},
		},
	})

	return string(env)
}

func TestEvidence(t *testing.T) {
	t.Parallel()
	srv := server(t)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	pubPEM := string(pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: der,
	}))

	keyBody, _ := json.Marshal(map[string]string{
		"alias": "ci-key",
		"key":   pubPEM,
	})
	if resp, body := do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/artifactory/api/security/keys/trusted",
			body:   string(keyBody),
		},
	); resp.StatusCode != 201 {
		t.Fatalf("add key = %d %s", resp.StatusCode, body)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/artifactory/api/security/keys/trusted",
			body:   string(keyBody),
		},
	); resp.StatusCode != 409 {
		t.Errorf("duplicate alias = %d, want 409", resp.StatusCode)
	}

	if _, body := do(t, srv, call{
		method: "GET",
		path:   "/artifactory/api/security/keys/trusted",
	}); !strings.Contains(
		body,
		`"alias": "ci-key"`,
	) {
		t.Errorf("keys = %s", body)
	}

	if _, v := do(t, srv, call{
		method: "GET",
		path:   "/evidence/api/v1/system/version",
	}); v != "7.646.1" {
		t.Errorf("version = %q", v)
	}

	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/app-local",
			body:   `{"rclass":"local","packageType":"generic"}`,
		},
	)
	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/app-local/dist/app.bin",
		body:   "binary",
	})

	sha := sha256Hex("binary")

	resp, created := do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/evidence/api/v1/subject/app-local/dist/app.bin",
			body:   envelope(t, key, "ci-key", sha),
		},
	)

	var c struct {
		Verified bool   `json:"verified"`
		Slug     string `json:"predicate_slug"`
		Repo     string `json:"repository"`
		Path     string `json:"path"`
		Name     string `json:"name"`
	}
	if err := json.Unmarshal([]byte(created), &c); resp.StatusCode != 201 || err != nil || !c.Verified ||
		c.Slug != "provenance" {
		t.Fatalf("create = %d %s", resp.StatusCode, created)
	}

	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if resp, body := do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/evidence/api/v1/subject/app-local/dist/app.bin",
			body:   envelope(t, other, "ci-key", sha),
		},
	); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "does not verify") {
		t.Errorf("wrong key = %d %s, want 400", resp.StatusCode, body)
	}

	if resp, body := do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/evidence/api/v1/subject/app-local/dist/app.bin",
			body:   envelope(t, key, "nope", sha),
		},
	); resp.StatusCode != 400 ||
		!strings.Contains(body, "not found") {
		t.Errorf("unknown alias = %d %s", resp.StatusCode, body)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/evidence/api/v1/subject/app-local/dist/app.bin",
			body:   envelope(t, key, "ci-key", sha256Hex("other")),
		},
	); resp.StatusCode != 400 {
		t.Errorf("digest mismatch = %d, want 400", resp.StatusCode)
	}

	if resp, body := do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/evidence/api/v1/subject/app-local/dist/missing.bin",
			body:   envelope(t, key, "ci-key", sha),
		},
	); resp.StatusCode != 404 ||
		!strings.Contains(body, "404 Not Found") {
		t.Errorf("missing subject = %d %s", resp.StatusCode, body)
	}

	q := `{"query":"{ evidence { searchEvidence( where: { hasSubjectWith: { repositoryKey: \"app-local\", path: \"dist\", name: \"app.bin\"}} ) { totalCount edges { node { downloadPath verified predicate subject { sha256 } signingKey { alias publicKey } } } } } }"}`
	resp, found := do(t, srv, call{
		method: "POST",
		path:   "/onemodel/api/v1/graphql",
		body:   q,
	})

	var res struct {
		Data struct {
			Evidence struct {
				SearchEvidence struct {
					TotalCount int `json:"totalCount"`
					Edges      []struct {
						Node struct {
							DownloadPath string          `json:"downloadPath"`
							Verified     bool            `json:"verified"`
							Predicate    json.RawMessage `json:"predicate"`
							Subject      struct {
								SHA256 string
							}
							SigningKey struct {
								Alias, PublicKey string
							}
						}
					}
				}
			}
		}
	}
	if err := json.Unmarshal([]byte(found), &res); resp.StatusCode != 200 || err != nil ||
		res.Data.Evidence.SearchEvidence.TotalCount != 1 {
		t.Fatalf("search = %d %s", resp.StatusCode, found)
	}

	stored := res.Data.Evidence.SearchEvidence.Edges[0].Node
	if !stored.Verified || stored.Subject.SHA256 != sha || stored.SigningKey.PublicKey != pubPEM ||
		!strings.Contains(string(stored.Predicate), "builder") {
		t.Errorf("node = %+v", stored)
	}

	if resp, file := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/" + stored.DownloadPath,
		},
	); resp.StatusCode != 200 ||
		!strings.Contains(file, `"payloadType"`) {
		t.Errorf("download evidence = %d", resp.StatusCode)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/onemodel/api/v1/graphql",
			body:   `{"query":"{ applications { list } }"}`,
		},
	); resp.StatusCode != 400 {
		t.Errorf("unsupported query = %d, want 400", resp.StatusCode)
	}

	create := `{"release_bundle_name":"app","release_bundle_version":"1","source_type":"artifacts","source":{"artifacts":[{"path":"app-local/dist/app.bin"}]}}`
	if resp, body := do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/lifecycle/api/v2/release_bundle?async=false",
			body:   create,
		},
	); resp.StatusCode != 201 {
		t.Fatalf("create bundle = %d %s", resp.StatusCode, body)
	}

	manifest := storageSHA256(t, srv, "release-bundles-v2/app/1/release-bundle.json.evd")
	if resp, body := do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/evidence/api/v1/subject/release-bundles-v2/app/1/release-bundle.json.evd",
			body:   envelope(t, key, "ci-key", manifest),
		},
	); resp.StatusCode != 201 {
		t.Fatalf("bundle evidence = %d %s", resp.StatusCode, body)
	}

	rbq := `{"query":"{ releaseBundleVersion { getVersion(repositoryKey: \"release-bundles-v2\", name: \"app\", version: \"1\") { evidenceConnection { edges { node { verified } } } artifactsConnection(first: 1000, after: \"YXJ0aWZhY3Q6MA==\", where: { hasEvidence: true }) { totalCount edges { node { sourceRepositoryPath } } } fromBuilds { name } } } }"}`

	_, rb := do(t, srv, call{
		method: "POST",
		path:   "/onemodel/api/v1/graphql",
		body:   rbq,
	})
	if !strings.Contains(rb, `"sourceRepositoryPath": "app-local/dist/app.bin"`) ||
		strings.Count(rb, `"verified": true`) < 2 {
		t.Errorf("bundle version = %s", rb)
	}
}

func storageSHA256(t *testing.T, srv *httptest.Server, path string) string {
	t.Helper()
	_, body := do(t, srv, call{
		method: "GET",
		path:   "/artifactory/api/storage/" + path,
	})

	var info struct {
		Checksums struct {
			SHA256 string
		} `json:"checksums"`
	}
	if err := json.Unmarshal([]byte(body), &info); err != nil || info.Checksums.SHA256 == "" {
		t.Fatalf("storage info of %s: %v: %s", path, err, body)
	}

	return info.Checksums.SHA256
}
