//go:build integration

package terraform_test

import (
	"archive/zip"
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"relic/internal/artifacts/formats/terraform"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
)

func TestConcurrentModuleCacheMissesFetchTheArchiveOnce(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	log := slog.New(slog.DiscardHandler)
	repos := repository.NewService(databasetest.NewDB(t), objectstoretest.NewMemory(), log)
	repos.AllowPrivateUpstreams(true)

	svc := terraform.NewService(repos, log)

	var archive bytes.Buffer

	zw := zip.NewWriter(&archive)

	w, err := zw.Create("label-0.25.0/main.tf")
	if err != nil {
		t.Fatal(err)
	}

	_, _ = w.Write([]byte(`variable "name" {}`))

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	var fetches atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/modules/acme/label/null/0.25.0/download":
			w.Header().Set("X-Terraform-Get", "/archives/label.zip")
			w.WriteHeader(http.StatusNoContent)
		case "/archives/label.zip":
			fetches.Add(1)
			time.Sleep(100 * time.Millisecond)

			_, _ = w.Write(archive.Bytes())
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	r := repository.Repository{
		Name:   "terraform-remote",
		Class:  repository.ClassRemote,
		Format: repository.FormatTerraform,
		Config: repository.Config{
			Remote: &repository.RemoteConfig{
				URL: srv.URL,
			},
		},
	}
	if err := repos.CreateRepository(ctx, r); err != nil {
		t.Fatal(err)
	}

	m, _ := terraform.ParseModule("acme", "label", "null")

	const clients = 8

	var wg sync.WaitGroup

	errs := make(chan error, clients)

	for range clients {
		wg.Go(func() {
			item, err := svc.RemoteModule(ctx, r, m, "0.25.0")
			if err == nil && item.Path() != "acme/label/null/0.25.0.zip" {
				t.Errorf("RemoteModule = %s", item.Path())
			}

			errs <- err
		})
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Errorf("RemoteModule = %v", err)
		}
	}

	if n := fetches.Load(); n != 1 {
		t.Errorf("%d concurrent cache misses fetched the archive %d times, want once", clients, n)
	}
}
