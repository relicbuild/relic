//go:build integration

package docker_test

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"relic/internal/artifacts/formats/docker"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
)

type registry struct {
	t     *testing.T
	svc   *docker.Service
	repos *repository.Service
}

func newRegistry(t *testing.T, repoKeys ...string) registry {
	t.Helper()

	log := slog.New(slog.DiscardHandler)
	blobs := objectstoretest.NewMemory()
	repos := repository.NewService(databasetest.NewDB(t), blobs, log)

	for _, key := range repoKeys {
		if err := repos.CreateRepository(
			t.Context(),
			repository.Repository{
				Name:   key,
				Class:  repository.ClassLocal,
				Format: docker.Format,
			},
		); err != nil {
			t.Fatal(err)
		}
	}

	return registry{
		t:     t,
		svc:   docker.NewService(repos, blobs, log),
		repos: repos,
	}
}

func (r registry) pushBlob(repoKey, image, content string) string {
	r.t.Helper()

	h := sha256Hex(content)
	if _, err := r.svc.FinishUpload(r.t.Context(), repoKey, image, "upload-"+h[:8], 0,
		strings.NewReader(content), int64(len(content)), h, "admin"); err != nil {
		r.t.Fatalf("push %q: %v", content, err)
	}

	return h
}

func (r registry) pushImage(repoKey, image, ref, layer string) string {
	r.t.Helper()

	manifest := r.imageManifest(repoKey, image, layer)
	r.putManifest(repoKey, image, ref, docker.MediaTypeOCIManifest, manifest)

	return manifest
}

func (r registry) imageManifest(repoKey, image, layer string) string {
	r.t.Helper()

	config := `{"architecture":"amd64","os":"linux"}`
	configDigest := r.pushBlob(repoKey, image, config)
	layerDigest := r.pushBlob(repoKey, image, layer)
	manifest := fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json",`+
		`"config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:%s","size":%d},`+
		`"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar","digest":"sha256:%s","size":%d}]}`,
		configDigest, len(config), layerDigest, len(layer))

	return manifest
}

func (r registry) putManifest(repoKey, image, ref, mediaType, manifest string) {
	r.t.Helper()

	if _, err := r.svc.PutManifest(r.t.Context(), repoKey, image, ref, mediaType, []byte(manifest), "admin",
		nil); err != nil {
		r.t.Fatalf("push %s:%s: %v", image, ref, err)
	}
}

func (r registry) manifestErr(repoKey, image, ref string) error {
	r.t.Helper()

	_, err := r.svc.GetManifest(r.t.Context(), repoKey, image, ref)

	return err
}

func (r registry) tags(repoKey, image string) []string {
	r.t.Helper()

	tags, err := r.svc.Tags(r.t.Context(), repoKey, image)
	if err != nil {
		r.t.Fatal(err)
	}

	return tags
}

func TestDeletingATagKeepsTheImageNamedAfterIt(t *testing.T) {
	t.Parallel()

	reg := newRegistry(t, "docker-local")
	reg.pushImage("docker-local", "app/b", "latest", "the nested image's layer")
	reg.pushImage("docker-local", "app", "b", "the tag's layer")

	if err := reg.svc.DeleteManifest(t.Context(), "docker-local", "app", "b"); err != nil {
		t.Fatal(err)
	}

	if err := reg.manifestErr("docker-local", "app", "b"); !errors.Is(err, docker.ErrManifestUnknown) {
		t.Errorf("app:b after its delete = %v, want ErrManifestUnknown", err)
	}

	if err := reg.manifestErr("docker-local", "app/b", "latest"); err != nil {
		t.Errorf("app/b:latest after deleting tag b of app: %v", err)
	}

	if got := reg.tags("docker-local", "app/b"); !slices.Equal(got, []string{
		"latest",
	}) {
		t.Errorf("tags of app/b = %v, want [latest]", got)
	}
}

func TestPromotingATagLeavesTheImageNamedAfterIt(t *testing.T) {
	t.Parallel()

	reg := newRegistry(t, "docker-local", "docker-prod")
	reg.pushImage("docker-local", "app/b", "latest", "the nested image's layer")
	reg.pushImage("docker-local", "app", "b", "the tag's layer")

	if err := reg.svc.Promote(t.Context(), docker.PromoteInput{
		SourceRepository: "docker-local",
		TargetRepository: "docker-prod",
		Image:            "app",
		Tag:              "b",
		User:             "admin",
	}); err != nil {
		t.Fatal(err)
	}

	if err := reg.manifestErr("docker-prod", "app", "b"); err != nil {
		t.Errorf("promoted app:b: %v", err)
	}

	if err := reg.manifestErr("docker-local", "app/b", "latest"); err != nil {
		t.Errorf("app/b:latest after promoting tag b of app: %v", err)
	}

	if err := reg.manifestErr("docker-prod", "app/b", "latest"); !errors.Is(err, docker.ErrManifestUnknown) {
		t.Errorf("app/b:latest in the target = %v, want ErrManifestUnknown", err)
	}
}

func TestManifestDigestsBelongToOneImage(t *testing.T) {
	t.Parallel()

	reg := newRegistry(t, "docker-local")
	manifest := reg.pushImage("docker-local", "app/b", "latest", "a shared layer")
	reg.pushImage("docker-local", "app/c", "only", "a shared layer")

	digest := "sha256:" + sha256Hex(manifest)

	if err := reg.manifestErr("docker-local", "app", digest); !errors.Is(err, docker.ErrManifestUnknown) {
		t.Errorf("app@digest held only by app/b and app/c = %v, want ErrManifestUnknown", err)
	}

	if err := reg.svc.DeleteManifest(t.Context(), "docker-local", "app", digest); !errors.Is(
		err,
		docker.ErrManifestUnknown,
	) {
		t.Errorf("deleting app@digest = %v, want ErrManifestUnknown", err)
	}

	for _, image := range []string{
		"app/b",
		"app/c",
	} {
		if err := reg.manifestErr("docker-local", image, digest); err != nil {
			t.Errorf("%s@digest: %v", image, err)
		}
	}

	if err := reg.svc.DeleteManifest(t.Context(), "docker-local", "app/b", digest); err != nil {
		t.Fatal(err)
	}

	if got := reg.tags("docker-local", "app/c"); !slices.Equal(got, []string{
		"only",
	}) {
		t.Errorf("tags of app/c after deleting app/b@digest = %v, want [only]", got)
	}
}

func TestMovingOneTagOfAnIndexKeepsTheOtherTagsPullable(t *testing.T) {
	t.Parallel()

	reg := newRegistry(t, "docker-local", "docker-prod")
	amd64 := reg.imageManifest("docker-local", "multi", "the amd64 layer")
	arm64 := reg.imageManifest("docker-local", "multi", "the arm64 layer")

	children := []string{
		"sha256:" + sha256Hex(amd64),
		"sha256:" + sha256Hex(arm64),
	}
	reg.putManifest("docker-local", "multi", children[0], docker.MediaTypeOCIManifest, amd64)
	reg.putManifest("docker-local", "multi", children[1], docker.MediaTypeOCIManifest, arm64)

	index := fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[`+
		`{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":%q,"size":%d,`+
		`"platform":{"os":"linux","architecture":"amd64"}},`+
		`{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":%q,"size":%d,`+
		`"platform":{"os":"linux","architecture":"arm64"}}]}`,
		children[0], len(amd64), children[1], len(arm64))
	for _, tag := range []string{
		"1",
		"latest",
	} {
		reg.putManifest("docker-local", "multi", tag, docker.MediaTypeOCIIndex, index)
	}

	promote := func(tag string) {
		t.Helper()

		if err := reg.svc.Promote(t.Context(), docker.PromoteInput{
			SourceRepository: "docker-local",
			TargetRepository: "docker-prod",
			Image:            "multi",
			Tag:              tag,
			User:             "admin",
		}); err != nil {
			t.Fatal(err)
		}
	}

	promote("1")

	for _, child := range children {
		if err := reg.manifestErr("docker-local", "multi", child); err != nil {
			t.Errorf("platform manifest %s of latest after moving tag 1: %v", child, err)
		}

		if err := reg.manifestErr("docker-prod", "multi", child); err != nil {
			t.Errorf("platform manifest %s of the moved tag 1: %v", child, err)
		}
	}

	promote("latest")

	for _, child := range children {
		if err := reg.manifestErr("docker-local", "multi", child); !errors.Is(err, docker.ErrManifestUnknown) {
			t.Errorf("platform manifest %s after moving every tag of it = %v, want ErrManifestUnknown", child, err)
		}

		if err := reg.manifestErr("docker-prod", "multi", child); err != nil {
			t.Errorf("platform manifest %s in the target: %v", child, err)
		}
	}
}
