//go:build integration

package docker_test

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"relic/internal/access/auth"
	"relic/internal/artifacts/formats/docker"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
)

func TestPromoteAsksBeforeOverwritingATag(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	log := slog.New(slog.DiscardHandler)
	blobs := objectstoretest.NewMemory()
	repos := repository.NewService(databasetest.NewDB(t), blobs, log)
	svc := docker.NewService(repos, blobs, log)

	for _, name := range []string{
		"docker-dev",
		"docker-prod",
	} {
		if err := repos.CreateRepository(
			ctx,
			repository.Repository{
				Name:   name,
				Class:  repository.ClassLocal,
				Format: docker.Format,
			},
		); err != nil {
			t.Fatal(err)
		}
	}

	pushImage := func(repo, architecture string) string {
		config := `{"architecture":"` + architecture + `","os":"linux"}`
		digest := sha256Hex(config)

		if _, err := svc.FinishUpload(ctx, repo, "app", "upload-"+digest[:8], 0,
			strings.NewReader(config), int64(len(config)), digest, "admin"); err != nil {
			t.Fatal(err)
		}

		manifest := fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json",`+
			`"config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:%s","size":%d},`+
			`"layers":[]}`, digest, len(config))

		if _, err := svc.PutManifest(ctx, repo, "app", "1.0", "application/vnd.oci.image.manifest.v1+json",
			[]byte(manifest), "admin", nil); err != nil {
			t.Fatal(err)
		}

		return sha256Hex(manifest)
	}
	promoted := pushImage("docker-dev", "arm64")
	original := pushImage("docker-prod", "amd64")

	tests := []struct {
		name         string
		canOverwrite bool
		wantErr      error
		wantDigest   string
	}{
		{
			name:       "overwrite refused",
			wantErr:    repository.ErrForbidden,
			wantDigest: original,
		},
		{
			name:         "overwrite allowed",
			canOverwrite: true,
			wantDigest:   promoted,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := svc.Promote(ctx, docker.PromoteInput{
				SourceRepository: "docker-dev",
				TargetRepository: "docker-prod",
				Image:            "app",
				Tag:              "1.0",
				Copy:             true,
				User:             "dev",
				Permits: func(_, _ string, action auth.Action) bool {
					return action != auth.ActionDelete || tt.canOverwrite
				},
			})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Promote = %v; want %v", err, tt.wantErr)
			}

			got, err := svc.GetManifest(ctx, "docker-prod", "app", "1.0")
			if err != nil {
				t.Fatal(err)
			}

			if got.Item.Checksums.SHA256 != tt.wantDigest {
				t.Errorf("docker-prod app:1.0 is %s; want %s", got.Item.Checksums.SHA256, tt.wantDigest)
			}
		})
	}
}
