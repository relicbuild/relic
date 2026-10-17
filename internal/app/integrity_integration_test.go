//go:build integration

package app

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore"
	"relic/internal/platform/objectstore/objectstoretest"
)

func TestDownloadOfCorruptContentFailsTheTransfer(t *testing.T) {
	t.Parallel()

	db := databasetest.NewDB(t)
	log := slog.New(slog.DiscardHandler)

	if err := BootstrapAdmin(t.Context(), db, "admin", "password", log); err != nil {
		t.Fatal(err)
	}

	tokens, err := NewTokens(t.Context(), db, nil)
	if err != nil {
		t.Fatal(err)
	}

	blobs := objectstoretest.NewFaulty(objectstoretest.NewMemory())
	srv := httptest.NewServer(NewHandler(db, blobs, tokens, nil, Options{}, log))
	t.Cleanup(srv.Close)

	createRepository(t, srv, "generic-local", "LOCAL", "GENERIC", "")
	upload(t, srv, "generic-local", "f.txt", "content served whole or not at all")

	tests := []struct {
		name  string
		fault objectstoretest.Fault
	}{
		{
			name: "corrupt",
			fault: objectstoretest.Fault{
				Op:      objectstoretest.OpGet,
				Corrupt: true,
			},
		},
		{
			name: "cut short",
			fault: objectstoretest.Fault{
				Op:        objectstoretest.OpGet,
				ShortRead: 5,
			},
		},
	}

	for _, tt := range tests {
		for _, path := range []string{
			"/api/v1/files/generic-local/f.txt",
			"/maven/generic-local/f.txt",
		} {
			t.Run(tt.name+" "+path, func(t *testing.T) {
				blobs.Inject(tt.fault)
				t.Cleanup(blobs.Clear)

				downloadMustFail(t, srv, path)
			})
		}
	}

	blobs.Inject(objectstoretest.Fault{
		Op:  objectstoretest.OpGet,
		Err: objectstore.ErrNotFound,
	})

	if resp, body := do(t, srv, call{
		method: http.MethodGet,
		path:   "/api/v1/files/generic-local/f.txt",
	}); resp.StatusCode != http.StatusInternalServerError || body == "" {
		t.Errorf("GET of missing content = %d %q, want 500", resp.StatusCode, body)
	}
}

func downloadMustFail(t *testing.T, srv *httptest.Server, path string) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}

	req.SetBasicAuth("admin", "password")

	resp, err := srv.Client().Do(req)
	if err != nil {
		return
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	if _, err := io.ReadAll(resp.Body); err == nil {
		t.Errorf("GET %s = %d with a complete-looking body, want the transfer to fail", path, resp.StatusCode)
	}
}
