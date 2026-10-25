//go:build integration

package integration

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"relic/api/adapters/artifactory/access"
	"relic/internal/access/auth"
	"relic/internal/app"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore"
	"relic/internal/platform/objectstore/objectstoretest"
)

func TestUploadsOverTheLimitAre413(t *testing.T) {
	t.Parallel()

	db := databasetest.NewDB(t)
	log := slog.New(slog.DiscardHandler)

	if err := app.BootstrapAdmin(t.Context(), db, "admin", "password", log); err != nil {
		t.Fatal(err)
	}

	tokens, err := auth.NewTokens(t.Context(), db, nil, access.Format{})
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(app.NewHandler(db, objectstoretest.NewMemory(), tokens, nil, app.Options{
		MaxUploadSize: 10,
	}, log))
	t.Cleanup(srv.Close)

	for name, format := range map[string]string{
		"generic-local": "generic",
		"docker-local":  "docker",
	} {
		if resp, body := do(t, srv, call{
			method: http.MethodPut,
			path:   "/artifactory/api/repositories/" + name,
			body:   `{"rclass":"local","packageType":"` + format + `"}`,
			headers: map[string]string{
				"Content-Type": "application/json",
			},
		}); resp.StatusCode != http.StatusOK {
			t.Fatalf("create %s = %d %s", name, resp.StatusCode, body)
		}
	}

	big := strings.Repeat("x", 11)

	for _, path := range []string{
		"/artifactory/generic-local/big.bin",
		"/api/v1/files/generic-local/big.bin",
	} {
		if resp, body := do(t, srv, call{
			method: http.MethodPut,
			path:   path,
			body:   big,
		}); resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Errorf("PUT %s of 11 bytes over a 10-byte limit = %d %s, want 413", path, resp.StatusCode, body)
		}
	}

	if resp, _ := do(t, srv, call{
		method: http.MethodPut,
		path:   "/artifactory/generic-local/small.bin",
		body:   strings.Repeat("x", 10),
	}); resp.StatusCode != http.StatusCreated {
		t.Errorf("PUT of exactly 10 bytes = %d, want 201", resp.StatusCode)
	}

	resp, _ := do(t, srv, call{
		method: http.MethodPost,
		path:   "/v2/docker-local/app/blobs/uploads/",
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("start upload = %d", resp.StatusCode)
	}

	if resp, body := do(t, srv, call{
		method: http.MethodPatch,
		path:   resp.Header.Get("Location"),
		body:   big,
	}); resp.StatusCode != http.StatusRequestEntityTooLarge || !strings.Contains(body, "SIZE_INVALID") {
		t.Errorf("PATCH of an 11-byte chunk = %d %s, want 413 SIZE_INVALID", resp.StatusCode, body)
	}
}

func faultyServer(t *testing.T) (*httptest.Server, *objectstoretest.Faulty) {
	t.Helper()

	db := databasetest.NewDB(t)
	log := slog.New(slog.DiscardHandler)

	if err := app.BootstrapAdmin(t.Context(), db, "admin", "password", log); err != nil {
		t.Fatal(err)
	}

	tokens, err := auth.NewTokens(t.Context(), db, nil, access.Format{})
	if err != nil {
		t.Fatal(err)
	}

	blobs := objectstoretest.NewFaulty(objectstoretest.NewMemory())
	srv := httptest.NewServer(app.NewHandler(db, blobs, tokens, nil, app.Options{}, log))
	t.Cleanup(srv.Close)

	return srv, blobs
}

func TestDeploysToAFullDatabaseAre507(t *testing.T) {
	t.Parallel()

	if !databasetest.SQLite() {
		t.Skip("a full disk is a single-node SQLite condition")
	}

	db := databasetest.NewDB(t)
	log := slog.New(slog.DiscardHandler)

	if err := app.BootstrapAdmin(t.Context(), db, "admin", "password", log); err != nil {
		t.Fatal(err)
	}

	tokens, err := auth.NewTokens(t.Context(), db, nil, access.Format{})
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(app.NewHandler(db, objectstoretest.NewMemory(), tokens, nil, app.Options{}, log))
	t.Cleanup(srv.Close)

	if resp, body := do(t, srv, call{
		method: http.MethodPut,
		path:   "/artifactory/api/repositories/generic-local",
		body:   `{"rclass":"local","packageType":"generic"}`,
		headers: map[string]string{
			"Content-Type": "application/json",
		},
	}); resp.StatusCode != http.StatusOK {
		t.Fatalf("create = %d %s", resp.StatusCode, body)
	}

	rows, err := db.Query(t.Context(), "PRAGMA max_page_count = 1")
	if err != nil {
		t.Fatal(err)
	}

	rows.Close()

	property := strings.Repeat("p", 3000)

	status := http.StatusCreated
	for i := 0; i < 200 && status == http.StatusCreated; i++ {
		resp, _ := do(t, srv, call{
			method: http.MethodPut,
			path:   fmt.Sprintf("/artifactory/generic-local/f%d.txt;p1=%s", i, property),
			body:   "content",
		})
		status = resp.StatusCode
	}

	if status != http.StatusInsufficientStorage {
		t.Errorf("deploying until the database is full ended with %d, want 507", status)
	}

	if resp, body := do(t, srv, call{
		method: http.MethodPut,
		path:   "/api/v1/files/generic-local/g.txt?p1=" + property,
		body:   "content",
	}); resp.StatusCode != http.StatusInsufficientStorage {
		t.Errorf("PUT /api/v1/files to a full database = %d %s, want 507", resp.StatusCode, body)
	}
}

func TestUploadsToAFullStoreAre507(t *testing.T) {
	t.Parallel()

	srv, blobs := faultyServer(t)

	if resp, body := do(t, srv, call{
		method: http.MethodPut,
		path:   "/artifactory/api/repositories/generic-local",
		body:   `{"rclass":"local","packageType":"generic"}`,
		headers: map[string]string{
			"Content-Type": "application/json",
		},
	}); resp.StatusCode != http.StatusOK {
		t.Fatalf("create = %d %s", resp.StatusCode, body)
	}

	blobs.Inject(objectstoretest.Fault{
		Op:  objectstoretest.OpPut,
		Err: objectstore.ErrStorageFull,
	})

	for _, path := range []string{
		"/artifactory/generic-local/f.bin",
		"/api/v1/files/generic-local/f.bin",
	} {
		if resp, body := do(t, srv, call{
			method: http.MethodPut,
			path:   path,
			body:   "content",
		}); resp.StatusCode != http.StatusInsufficientStorage {
			t.Errorf("PUT %s to a full store = %d %s, want 507", path, resp.StatusCode, body)
		}
	}
}

func TestDeploysPastTheQuotaAre413(t *testing.T) {
	t.Parallel()

	srv := server(t)
	if resp, body := do(t, srv, call{
		method: http.MethodPost,
		path:   "/api/v1/repositories",
		body:   `{"name":"small","class":"REPOSITORY_CLASS_LOCAL","format":"FORMAT_GENERIC","maxStorage":"10"}`,
		headers: map[string]string{
			"Content-Type": "application/json",
		},
	}); resp.StatusCode != http.StatusOK {
		t.Fatalf("create = %d %s", resp.StatusCode, body)
	}

	if resp, _ := do(t, srv, call{
		method: http.MethodPut,
		path:   "/artifactory/small/a.bin",
		body:   "eight b.",
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("first deploy = %d", resp.StatusCode)
	}

	for _, path := range []string{
		"/artifactory/small/b.bin",
		"/api/v1/files/small/b.bin",
	} {
		if resp, body := do(t, srv, call{
			method: http.MethodPut,
			path:   path,
			body:   "eight b!",
		}); resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Errorf("PUT %s past the quota = %d %s, want 413", path, resp.StatusCode, body)
		}
	}

	if _, body := do(t, srv, call{
		method: http.MethodGet,
		path:   "/api/v1/repositories/small",
	}); !strings.Contains(body, `"usedBytes":"8"`) {
		t.Errorf("repository = %s, want usedBytes 8", body)
	}
}

func TestArchiveErrorsKeepStorageDetailsFromTheClient(t *testing.T) {
	t.Parallel()

	srv, blobs := faultyServer(t)
	do(t, srv, call{
		method: http.MethodPut,
		path:   "/artifactory/api/repositories/generic-local",
		body:   `{"rclass":"local","packageType":"generic"}`,
	})

	var zipped bytes.Buffer

	zw := zip.NewWriter(&zipped)

	w, err := zw.Create("readme.txt")
	if err != nil {
		t.Fatal(err)
	}

	_, _ = io.WriteString(w, "read me")
	_ = zw.Close()

	do(t, srv, call{
		method: http.MethodPut,
		path:   "/artifactory/generic-local/app.zip",
		body:   zipped.String(),
	})

	const storageDetail = "bucket relic-artifacts at storage.internal.example.com refused the key"

	blobs.Inject(objectstoretest.Fault{
		Op:  objectstoretest.OpGet,
		Err: errors.New(storageDetail),
	})

	for _, c := range []call{
		{
			method: http.MethodPost,
			path:   "/api/relic.v1.ArtifactService/ListArchiveEntries",
			body:   `{"repository":"generic-local","path":"app.zip"}`,
			headers: map[string]string{
				"Content-Type": "application/json",
			},
		},
		{
			method: http.MethodGet,
			path:   "/api/v1/archive/generic-local/app.zip?entry=readme.txt",
		},
	} {
		if resp, body := do(t, srv, c); resp.StatusCode != http.StatusInternalServerError ||
			strings.Contains(body, "storage.internal") {
			t.Errorf(
				"%s %s with a failing store = %d %s, want 500 without the store's error",
				c.method,
				c.path,
				resp.StatusCode,
				body,
			)
		}
	}
}

func TestDownloadErrorsCarryTheirOwnLength(t *testing.T) {
	t.Parallel()

	srv, blobs := faultyServer(t)
	do(t, srv, call{
		method: http.MethodPut,
		path:   "/artifactory/api/repositories/docker-local",
		body:   `{"rclass":"local","packageType":"docker"}`,
	})

	digest := pushBlob(t, srv, "docker-local/app", "a layer of the image")

	blobs.Inject(objectstoretest.Fault{
		Op: objectstoretest.OpGet,
	})

	req, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		srv.URL+"/v2/docker-local/app/blobs/"+digest,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	req.SetBasicAuth("admin", "password")

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	if _, err := io.ReadAll(resp.Body); err != nil || resp.StatusCode == http.StatusOK {
		t.Errorf(
			"a blob the store cannot read = %d, body read %v; want an error response read whole",
			resp.StatusCode,
			err,
		)
	}
}
