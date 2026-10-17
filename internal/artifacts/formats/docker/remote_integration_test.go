//go:build integration

package docker

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"relic/internal/artifacts/repository"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
)

func TestLinkBlobReadsEachManifestOnce(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	log := slog.New(slog.DiscardHandler)
	blobs := objectstoretest.NewFaulty(objectstoretest.NewMemory())
	repos := repository.NewService(databasetest.NewDB(t), blobs, log)
	svc := NewService(repos, blobs, log)

	if err := repos.CreateRepository(
		ctx,
		repository.Repository{
			Name:   "docker-local",
			Class:  repository.ClassLocal,
			Format: Format,
		},
	); err != nil {
		t.Fatal(err)
	}

	push := func(content string) string {
		sum := sha256.Sum256([]byte(content))
		digest := hex.EncodeToString(sum[:])

		if _, err := svc.FinishUpload(ctx, "docker-local", "app", "upload-"+digest[:8], 0,
			strings.NewReader(content), int64(len(content)), digest, "admin"); err != nil {
			t.Fatalf("push %q: %v", content, err)
		}

		return digest
	}

	config := `{"architecture":"amd64","os":"linux"}`
	configDigest := push(config)
	first := push("the first layer")
	second := push("the second layer")

	manifest := fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json",`+
		`"config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:%s","size":%d},`+
		`"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar","digest":"sha256:%s","size":%d},`+
		`{"mediaType":"application/vnd.oci.image.layer.v1.tar","digest":"sha256:%s","size":%d}]}`,
		configDigest, len(config), first, len("the first layer"), second, len("the second layer"))
	if _, err := svc.PutManifest(ctx, "docker-local", "app", "1.0", "application/vnd.oci.image.manifest.v1+json",
		[]byte(manifest), "admin", nil); err != nil {
		t.Fatal(err)
	}

	svc.linkBlob(ctx, "docker-local", "app", first)

	before := blobs.Calls(objectstoretest.OpGet)

	svc.linkBlob(ctx, "docker-local", "app", second)

	if reads := blobs.Calls(objectstoretest.OpGet) - before; reads != 0 {
		t.Errorf("linking a second blob read %d objects, want the manifest's blobs remembered", reads)
	}
}
