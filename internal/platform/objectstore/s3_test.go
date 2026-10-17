package objectstore

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type recordedRequest struct {
	method, path  string
	authorization string
	header        http.Header
}

func fakeS3(t *testing.T) (*httptest.Server, func() []recordedRequest) {
	t.Helper()

	var (
		mu   sync.Mutex
		seen []recordedRequest
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)

		mu.Lock()

		seen = append(seen, recordedRequest{
			method:        r.Method,
			path:          r.URL.Path,
			authorization: r.Header.Get("Authorization"),
			header:        r.Header.Clone(),
		})
		mu.Unlock()

		switch r.Method {
		case http.MethodHead:
			w.Header().Set("Content-Length", "4")
		case http.MethodPut:
			if r.Header.Get("X-Amz-Copy-Source") != "" {
				_, _ = io.WriteString(w, `<CopyObjectResult><ETag>"e"</ETag></CopyObjectResult>`)
				return
			}

			w.Header().Set("ETag", `"e"`)
		}
	}))
	t.Cleanup(srv.Close)

	return srv, func() []recordedRequest {
		mu.Lock()
		defer mu.Unlock()

		return append([]recordedRequest(nil), seen...)
	}
}

func TestS3StoreSendsEncryptionAndStorageClass(t *testing.T) {
	t.Parallel()

	srv, requests := fakeS3(t)
	s := NewS3(S3Config{
		Endpoint:             srv.URL,
		Region:               "eu-west-1",
		AccessKey:            "static",
		SecretKey:            "static-secret",
		ArtifactsBucket:      "blobs",
		TemporaryBucket:      "tmp",
		ServerSideEncryption: "aws:kms",
		SSEKMSKeyID:          "alias/relic",
		StorageClass:         "STANDARD_IA",
	})

	if err := s.Put(t.Context(), TemporaryKeyPrefix+"up", strings.NewReader("data"), 4); err != nil {
		t.Fatal(err)
	}

	if err := s.Copy(t.Context(), TemporaryKeyPrefix+"up", Key("ab"+strings.Repeat("0", 62))); err != nil {
		t.Fatal(err)
	}

	writes := 0

	for _, r := range requests() {
		if r.method != http.MethodPut {
			continue
		}

		writes++

		if r.header.Get("X-Amz-Server-Side-Encryption") != "aws:kms" ||
			r.header.Get("X-Amz-Server-Side-Encryption-Aws-Kms-Key-Id") != "alias/relic" ||
			r.header.Get("X-Amz-Storage-Class") != "STANDARD_IA" {
			t.Errorf("%s %s headers = %v; want SSE-KMS and STANDARD_IA", r.method, r.path, r.header)
		}
	}

	if writes != 2 {
		t.Errorf("%d writes, want a put and a copy", writes)
	}
}

func TestS3StoreSignsWithTheDefaultCredentialChain(t *testing.T) {
	srv, requests := fakeS3(t)

	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDFROMTHECHAIN")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "chain-secret")
	t.Setenv("AWS_CONFIG_FILE", t.TempDir()+"/none")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", t.TempDir()+"/none")

	s, err := Open(t.Context(), Config{
		Type: TypeS3,
		S3: S3Config{
			Endpoint:        srv.URL,
			Region:          "eu-west-1",
			ArtifactsBucket: "blobs",
			TemporaryBucket: "tmp",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Head(t.Context(), TemporaryKeyPrefix+"x"); err != nil {
		t.Fatal(err)
	}

	got := requests()
	if len(got) != 1 || !strings.Contains(got[0].authorization, "Credential=AKIDFROMTHECHAIN/") {
		t.Errorf("requests = %+v; want one signed with the environment's key", got)
	}
}

func TestS3StoreCopyPartsFitTheLimit(t *testing.T) {
	t.Parallel()

	s := &S3Store{
		maxUploadParts: 10_000,
	}

	for _, size := range []int64{
		1 << 20,
		1 << 40,
		5 << 40,
	} {
		part := s.copyPartSize(size)
		if parts := (size + part - 1) / part; parts > s.maxUploadParts || part < minCopyPartSize {
			t.Errorf("copy of %d bytes in parts of %d: %d parts, want at most %d of at least %d",
				size, part, parts, s.maxUploadParts, minCopyPartSize)
		}
	}
}
