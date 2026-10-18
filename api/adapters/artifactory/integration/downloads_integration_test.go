//go:build integration

package integration

import (
	"log/slog"
	"net/http/httptest"
	"testing"

	"relic/api/adapters/artifactory/access"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
	"relic/internal/access/auth"
	"relic/internal/app"
	"relic/internal/artifacts/downloadstats"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
)

func TestDownloadStats(t *testing.T) {
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

	rec := downloadstats.NewRecorder(db, log)
	srv := httptest.NewServer(
		app.NewHandler(db, objectstoretest.NewMemory(), tokens, nil, app.Options{
			Downloads: rec,
		}, log),
	)
	t.Cleanup(srv.Close)

	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/libs",
			body:   `{"rclass":"local","packageType":"generic"}`,
		},
	)
	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/libs/a.txt",
		body:   "hello",
	})
	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/libs/gone.txt",
		body:   "bye",
	})

	for _, c := range []call{
		{
			method: "GET",
			path:   "/artifactory/libs/a.txt",
		},
		{
			method: "GET",
			path:   "/artifactory/libs/a.txt",
		},
		{
			method: "HEAD",
			path:   "/artifactory/libs/a.txt",
		},
		{
			method: "GET",
			path:   "/artifactory/libs/a.txt",
			headers: map[string]string{
				"Range": "bytes=0-1",
			},
		},
		{
			method: "GET",
			path:   "/artifactory/libs/gone.txt",
		},
	} {
		do(t, srv, c)
	}

	do(t, srv, call{
		method: "DELETE",
		path:   "/artifactory/libs/gone.txt",
	})

	if err := rec.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}

	arts := relicv1connect.NewArtifactServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))

	downloads := func() *relicv1.DownloadStats {
		t.Helper()

		d, err := arts.GetItemDetails(
			t.Context(),
			&relicv1.GetItemDetailsRequest{
				Repository: "libs",
				Path:       "a.txt",
			},
		)
		if err != nil {
			t.Fatal(err)
		}

		return d.GetDownloads()
	}
	if d := downloads(); d.GetCount() != 2 || d.GetLastDownloadedBy() != "admin" {
		t.Errorf("downloads = %v", d)
	}

	do(t, srv, call{
		method: "GET",
		path:   "/artifactory/libs/a.txt",
	})

	if err := rec.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}

	if d := downloads(); d.GetCount() != 3 {
		t.Errorf("downloads after a second batch = %v", d)
	}

	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/tf-local",
			body:   `{"rclass":"local","packageType":"terraform"}`,
		},
	)
	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/tf-local/acme/network/aws/v1.0.0.zip",
		body:   "zip v1.0.0",
	})

	resp, _ := do(t, srv, call{
		method: "GET",
		path:   "/artifactory/api/terraform/v1/modules/tf-local__acme/network/aws/1.0.0/download",
	})
	if resp, _ := do(t, srv, call{
		method: "GET",
		path:   resp.Header.Get("X-Terraform-Get"),
		noAuth: true,
	}); resp.StatusCode != 200 {
		t.Fatalf("signed module archive = %d", resp.StatusCode)
	}

	if err := rec.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}

	module, err := arts.GetItemDetails(
		t.Context(),
		&relicv1.GetItemDetailsRequest{
			Repository: "tf-local",
			Path:       "acme/network/aws/v1.0.0.zip",
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if d := module.GetDownloads(); d.GetCount() != 1 || d.GetLastDownloadedBy() != "admin" {
		t.Errorf("signed module download = %v, want one by admin", d)
	}
}
