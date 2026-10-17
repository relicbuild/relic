//go:build integration

package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"relic/internal/platform/database"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"

	"connectrpc.com/connect"
)

func TestMain(m *testing.M) { databasetest.Main(m) }

func createRepository(t *testing.T, srv *httptest.Server, name, class, format, extra string) {
	t.Helper()

	body := `{"name":"` + name + `","class":"REPOSITORY_CLASS_` + class + `","format":"FORMAT_` + format + `"` + extra + `}`
	if resp, b := do(
		t,
		srv,
		call{
			method: http.MethodPost,
			path:   "/api/v1/repositories",
			body:   body,
			headers: map[string]string{
				"Content-Type": "application/json",
			},
		},
	); resp.StatusCode != http.StatusOK {
		t.Fatalf("create %s = %d %s", name, resp.StatusCode, b)
	}
}

func upload(t *testing.T, srv *httptest.Server, repo, path, content string) {
	t.Helper()

	if resp, b := do(
		t,
		srv,
		call{
			method: http.MethodPut,
			path:   "/api/v1/files/" + repo + "/" + path,
			body:   content,
		},
	); resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload %s/%s = %d %s", repo, path, resp.StatusCode, b)
	}
}

func server(t *testing.T) *httptest.Server {
	t.Helper()
	srv, _ := serverDB(t)

	return srv
}

func serverDB(t *testing.T) (*httptest.Server, *database.DB) {
	t.Helper()
	db := databasetest.NewDB(t)

	log := slog.New(slog.DiscardHandler)
	if err := BootstrapAdmin(t.Context(), db, "admin", "password", log); err != nil {
		t.Fatal(err)
	}

	tokens, err := NewTokens(t.Context(), db, nil)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(
		NewHandler(db, objectstoretest.NewMemory(), tokens, nil, Options{
			AllowPrivateUpstreams: true,
		}, log),
	)
	t.Cleanup(srv.Close)

	return srv, db
}

type call struct {
	method, path string
	body         string
	headers      map[string]string
	noAuth       bool
}

type reply struct {
	StatusCode int
	Header     http.Header
}

func do(t *testing.T, srv *httptest.Server, c call) (reply, string) {
	t.Helper()

	var body io.Reader
	if c.body != "" {
		body = strings.NewReader(c.body)
	}

	req, err := http.NewRequestWithContext(t.Context(), c.method, srv.URL+c.path, body)
	if err != nil {
		t.Fatal(err)
	}

	if !c.noAuth {
		req.SetBasicAuth("admin", "password")
	}

	for k, v := range c.headers {
		req.Header.Set(k, v)
	}

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	return reply{
		StatusCode: resp.StatusCode,
		Header:     resp.Header,
	}, string(b)
}

func hexSum(sum []byte) string { return hex.EncodeToString(sum) }

func npmTarball(t *testing.T, name, version string) []byte {
	t.Helper()

	var buf bytes.Buffer

	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	pkg := `{"name":"` + name + `","version":"` + version + `","description":"test package"}`
	for file, content := range map[string]string{
		"package/package.json": pkg,
		"package/index.js":     "module.exports = 1",
	} {
		_ = tw.WriteHeader(&tar.Header{
			Name:     file,
			Mode:     0o644,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
		})
		_, _ = tw.Write([]byte(content))
	}

	_ = tw.Close()
	_ = gz.Close()

	return buf.Bytes()
}

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

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

type asUser struct {
	user, password string
}

func (a asUser) RoundTrip(r *http.Request) (*http.Response, error) {
	if a.user != "" {
		r = r.Clone(r.Context())
		r.SetBasicAuth(a.user, a.password)
	}

	return http.DefaultTransport.RoundTrip(r)
}

func relicHTTP(user, password string) *http.Client {
	return &http.Client{
		Transport: asUser{
			user,
			password,
		},
	}
}

func code(err error) connect.Code {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Code()
	}

	return 0
}
