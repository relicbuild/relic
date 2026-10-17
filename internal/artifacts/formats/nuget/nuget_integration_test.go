//go:build integration

package nuget_test

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"relic/internal/artifacts/formats/nuget"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
)

func TestMain(m *testing.M) { databasetest.Main(m) }

func nupkgOf(t *testing.T, id, version string) []byte {
	t.Helper()

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)

	w, err := zw.Create(id + ".nuspec")
	if err != nil {
		t.Fatal(err)
	}

	_, _ = w.Write([]byte(`<?xml version="1.0"?><package><metadata><id>` + id + `</id><version>` + version +
		`</version><dependencies><dependency id="Dep" version="1.0.0" /></dependencies></metadata></package>`))

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func newNuGet(t *testing.T) (*repository.Service, *nuget.Service, *objectstoretest.Faulty, repository.Repository) {
	t.Helper()

	log := slog.New(slog.DiscardHandler)
	blobs := objectstoretest.NewFaulty(objectstoretest.NewMemory())
	repos := repository.NewService(databasetest.NewDB(t), blobs, log)
	local := repository.Repository{
		Name:   "nuget-local",
		Class:  repository.ClassLocal,
		Format: nuget.Format,
	}

	if err := repos.CreateRepository(t.Context(), local); err != nil {
		t.Fatal(err)
	}

	return repos, nuget.NewService(repos, log), blobs, local
}

func TestConcurrentPushesOfOneVersionStoreItOnce(t *testing.T) {
	t.Parallel()

	_, svc, _, _ := newNuGet(t)
	body := nupkgOf(t, "Demo", "1.0.0")

	const pushes = 3

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		stored  int
		refused int
	)

	for i := range pushes {
		file := filepath.Join(t.TempDir(), "push.nupkg")
		if err := os.WriteFile(file, body, 0o600); err != nil {
			t.Fatal(err)
		}

		wg.Go(func() {
			f, err := os.Open(file)
			if err != nil {
				t.Error(err)
				return
			}

			defer func() {
				_ = f.Close()
			}()

			_, err = svc.Push(t.Context(), "nuget-local", f, "admin")

			mu.Lock()
			defer mu.Unlock()

			switch {
			case err == nil:
				stored++
			case errors.Is(err, nuget.ErrExists):
				refused++
			default:
				t.Errorf("push %d: %v", i, err)
			}
		})
	}

	wg.Wait()

	if stored != 1 || refused != pushes-1 {
		t.Errorf("concurrent pushes stored %d and refused %d, want 1 and %d", stored, refused, pushes-1)
	}
}

func TestV2Entries(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	repos, svc, blobs, local := newNuGet(t)

	if _, err := repos.Deploy(ctx, repository.DeployInput{
		Repository: "nuget-local",
		Path:       "uploads/demo-latest.nupkg",
		Body:       bytes.NewReader(nupkgOf(t, "Demo", "1.0.0")),
		User:       "admin",
	}); err != nil {
		t.Fatal(err)
	}

	entries, err := svc.V2Entries(ctx, local, "Demo")
	if err != nil || len(entries) != 1 || entries[0].Version != "1.0.0" || entries[0].Dependencies == "" {
		t.Fatalf("V2Entries of a package deployed under another name = %+v, %v", entries, err)
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/flatcontainer/broken/index.json":
			_, _ = io.WriteString(w, `{"versions":["1.0.0"]}`)
		case "/flatcontainer/broken/1.0.0/broken.nuspec":
			_, _ = io.WriteString(w, "<package><metadata>")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(upstream.Close)

	repos.AllowPrivateUpstreams(true)

	remote := repository.Repository{
		Name:   "nuget-remote",
		Class:  repository.ClassRemote,
		Format: nuget.Format,
		Config: repository.Config{
			Remote: &repository.RemoteConfig{
				URL: upstream.URL,
			},
		},
	}
	if err := repos.CreateRepository(ctx, remote); err != nil {
		t.Fatal(err)
	}

	broken, err := svc.V2Entries(ctx, remote, "Broken")
	if err != nil || len(broken) != 1 || broken[0].Version != "1.0.0" || broken[0].Dependencies != "" {
		t.Errorf(
			"V2Entries of a remote package whose .nuspec is malformed = %+v, %v; want the version listed",
			broken,
			err,
		)
	}

	blobs.Inject(objectstoretest.Fault{
		Op: objectstoretest.OpGet,
	})

	if entries, err := svc.V2Entries(ctx, local, "Demo"); err == nil {
		t.Errorf("V2Entries with unreadable package metadata = %+v, nil; want the error", entries)
	}
}
