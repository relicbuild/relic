//go:build integration

package nuget_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"

	"relic/internal/artifacts/formats/nuget"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore"
	"relic/internal/platform/objectstore/objectstoretest"
)

type countingStore struct {
	objectstore.Store

	read atomic.Int64
}

func (s *countingStore) Get(
	ctx context.Context,
	key string,
	rng *objectstore.Range,
) (io.ReadCloser, objectstore.Info, error) {
	body, info, err := s.Store.Get(ctx, key, rng)
	if err != nil {
		return nil, info, err
	}

	return countingReader{
		ReadCloser: body,
		read:       &s.read,
	}, info, nil
}

type countingReader struct {
	io.ReadCloser

	read *atomic.Int64
}

func (r countingReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.read.Add(int64(n))

	return n, err
}

func largeNupkg(t *testing.T, id, version string, payload int) []byte {
	t.Helper()

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)

	w, err := zw.Create(id + ".nuspec")
	if err != nil {
		t.Fatal(err)
	}

	_, _ = w.Write([]byte(`<?xml version="1.0"?><package><metadata><id>` + id + `</id><version>` + version +
		`</version></metadata></package>`))

	lib, err := zw.CreateHeader(&zip.FileHeader{
		Name:   "lib/net8.0/" + id + ".dll",
		Method: zip.Store,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := io.CopyN(lib, rand.Reader, int64(payload)); err != nil {
		t.Fatal(err)
	}

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func TestNuspecReadsOnlyTheManifest(t *testing.T) {
	t.Parallel()

	const payload = 8 << 20

	ctx := t.Context()
	log := slog.New(slog.DiscardHandler)
	blobs := &countingStore{
		Store: objectstoretest.NewMemory(),
	}
	repos := repository.NewService(databasetest.NewDB(t), blobs, log)

	if err := repos.CreateRepository(ctx, repository.Repository{
		Name:   "nuget-local",
		Class:  repository.ClassLocal,
		Format: nuget.Format,
	}); err != nil {
		t.Fatal(err)
	}

	svc := nuget.NewService(repos, log)

	item, err := repos.Deploy(ctx, repository.DeployInput{
		Repository: "nuget-local",
		Path:       "big/1.0.0/big.1.0.0.nupkg",
		Body:       bytes.NewReader(largeNupkg(t, "Big", "1.0.0", payload)),
		User:       "admin",
	})
	if err != nil {
		t.Fatal(err)
	}

	if read := blobs.read.Load(); read > 1<<20 {
		t.Errorf("indexing a deployed package read %d bytes of it, want its manifest only", read)
	}

	blobs.read.Store(0)

	for range 3 {
		raw, n, err := svc.Nuspec(ctx, item)
		if err != nil || n.Metadata.ID != "Big" || !bytes.Contains(raw, []byte("<id>Big</id>")) {
			t.Fatalf("Nuspec = %q, %+v, %v", raw, n.Metadata, err)
		}
	}

	if read := blobs.read.Load(); read > 1<<20 {
		t.Errorf("three Nuspec calls read %d bytes of an %d-byte package, want its manifest once", read, payload)
	}

	blobs.read.Store(0)

	if _, _, err := svc.Nuspec(ctx, item); err != nil || blobs.read.Load() != 0 {
		t.Errorf("a cached Nuspec read %d bytes, %v; want none", blobs.read.Load(), err)
	}
}
