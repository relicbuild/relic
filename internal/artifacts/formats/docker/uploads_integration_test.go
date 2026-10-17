//go:build integration

package docker_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"relic/internal/artifacts/formats/docker"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
)

func TestMain(m *testing.M) { databasetest.Main(m) }

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))

	return hex.EncodeToString(sum[:])
}

func TestExpireUploadsKeepsWhatManifestsReference(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	log := slog.New(slog.DiscardHandler)
	blobs := objectstoretest.NewMemory()
	repos := repository.NewService(databasetest.NewDB(t), blobs, log)
	svc := docker.NewService(repos, blobs, log)

	if err := repos.CreateRepository(
		ctx,
		repository.Repository{
			Name:   "docker-local",
			Class:  repository.ClassLocal,
			Format: docker.Format,
		},
	); err != nil {
		t.Fatal(err)
	}

	push := func(content string) string {
		h := sha256Hex(content)
		if _, err := svc.FinishUpload(ctx, "docker-local", "app", "upload-"+h[:8], 0,
			strings.NewReader(content), int64(len(content)), h, "admin"); err != nil {
			t.Fatalf("push %q: %v", content, err)
		}

		return h
	}

	orphan := push("a layer no manifest uses")
	layer := push("a layer the manifest uses")
	config := push(`{"architecture":"amd64","os":"linux"}`)

	manifest := fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json",`+
		`"config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:%s","size":%d},`+
		`"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar","digest":"sha256:%s","size":%d}]}`,
		config, len(`{"architecture":"amd64","os":"linux"}`), layer, len("a layer the manifest uses"))
	if _, err := svc.PutManifest(ctx, "docker-local", "app", "1.0", "application/vnd.oci.image.manifest.v1+json",
		[]byte(manifest), "admin", nil); err != nil {
		t.Fatal(err)
	}

	n, err := docker.ExpireUploads(ctx, repos, 0)
	if n != 1 || err != nil {
		t.Fatalf("ExpireUploads = %d, %v; want the one unreferenced upload", n, err)
	}

	if _, err := svc.Blob(ctx, "docker-local", "app", orphan); !errors.Is(err, docker.ErrBlobUnknown) {
		t.Errorf("the unreferenced blob is still served: %v", err)
	}

	for _, h := range []string{
		layer,
		config,
	} {
		if _, err := svc.Blob(ctx, "docker-local", "app", h); err != nil {
			t.Errorf("blob %s of the manifest: %v", h[:8], err)
		}
	}
}
