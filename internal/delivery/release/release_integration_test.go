//go:build integration

package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
	"uuid"

	"relic/internal/artifacts/repository"
	"relic/internal/artifacts/signingkey"
	"relic/internal/platform/database"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore"
	"relic/internal/platform/objectstore/objectstoretest"
)

type releaseFixture struct {
	releases *Service
	repos    *repository.Service
	blobs    *objectstoretest.Memory
	db       *database.DB
}

func newReleaseFixture(t *testing.T) releaseFixture {
	t.Helper()

	ctx := t.Context()
	db := databasetest.NewDB(t)
	log := slog.New(slog.DiscardHandler)
	blobs := objectstoretest.NewMemory()
	repos := repository.NewService(db, blobs, log)

	for _, r := range []repository.Repository{
		{
			Name:   "libs",
			Class:  repository.ClassLocal,
			Format: "generic",
		},
		{
			Name:   "prod",
			Class:  repository.ClassLocal,
			Format: "generic",
			Config: repository.Config{
				Environments: []string{
					"PROD",
				},
			},
		},
	} {
		if err := repos.CreateRepository(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	f := releaseFixture{
		releases: New(db, repos, nil, signingkey.New(db, nil), log),
		repos:    repos,
		blobs:    blobs,
		db:       db,
	}
	f.deploy(t, "libs", "app.bin", "application")
	f.deploy(t, "libs", "other.bin", "other")

	return f
}

func (f releaseFixture) deploy(t *testing.T, repo, path, body string) repository.Item {
	t.Helper()

	item, err := f.repos.Deploy(t.Context(), repository.DeployInput{
		Repository: repo,
		Path:       path,
		Body:       strings.NewReader(body),
	})
	if err != nil {
		t.Fatal(err)
	}

	return item
}

func artifactSource(paths ...string) []Source {
	refs := make([]ArtifactRef, 0, len(paths))
	for _, p := range paths {
		refs = append(refs, ArtifactRef{
			Path: p,
		})
	}

	return []Source{
		{
			Kind:      SourceArtifacts,
			Artifacts: refs,
		},
	}
}

func (f releaseFixture) create(t *testing.T, version string, draft bool, sources []Source) Release {
	t.Helper()

	b, err := f.releases.Create(t.Context(), "", Spec{
		Name:    "app",
		Version: version,
		Draft:   draft,
		Sources: sources,
	}, "admin", Access{})
	if err != nil {
		t.Fatal(err)
	}

	return b
}

func TestPromoteRefusesAReleaseStillProcessing(t *testing.T) {
	t.Parallel()

	f := newReleaseFixture(t)
	if err := f.releases.insert(t.Context(), Release{
		ID:         uuid.NewV7(),
		Repository: f.releases.layout.Repository(""),
		Name:       "app",
		Version:    "1.0",
		Status:     StatusProcessing,
		CreatedBy:  "admin",
		Created:    time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	_, err := f.releases.Promote(t.Context(), "", "app", "1.0", PromotionSpec{
		Environment: "PROD",
	}, "admin", Access{})
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("Promote of a processing release = %v; want ErrInvalid", err)
	}
}

func TestPromoteSaysWhenItsFiltersLeaveNoRepository(t *testing.T) {
	t.Parallel()

	f := newReleaseFixture(t)
	f.create(t, "1.0", false, artifactSource("libs/app.bin"))

	_, err := f.releases.Promote(t.Context(), "", "app", "1.0", PromotionSpec{
		Environment: "PROD",
		Exclude: []string{
			"prod",
		},
	}, "admin", Access{})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "include and exclude") {
		t.Errorf("Promote with every PROD repository excluded = %v; want the filters named", err)
	}
}

func TestPromotionsInOneMillisecondAreBothRecorded(t *testing.T) {
	t.Parallel()

	f := newReleaseFixture(t)
	f.create(t, "1.0", false, artifactSource("libs/app.bin"))

	now := time.Now()
	f.releases.now = func() time.Time {
		return now
	}

	for range 2 {
		if _, err := f.releases.Promote(t.Context(), "", "app", "1.0", PromotionSpec{
			Environment: "PROD",
		}, "admin", Access{}); err != nil {
			t.Fatal(err)
		}
	}

	b, err := f.releases.Get(t.Context(), "", "app", "1.0")
	if err != nil {
		t.Fatal(err)
	}

	promotions, err := f.db.ListReleasePromotions(t.Context(), b.ID)
	if err != nil {
		t.Fatal(err)
	}

	if len(promotions) != 2 {
		t.Errorf("promotions = %d; want 2", len(promotions))
	}
}

func TestGateErrorsNameTheGate(t *testing.T) {
	t.Parallel()

	f := newReleaseFixture(t)
	ctx := t.Context()

	_, getErr := f.releases.Gate(ctx, "nonexistent")
	deleteErr := f.releases.DeleteGate(ctx, "nonexistent")
	_, noEnvironment := f.releases.PutGate(ctx, Gate{
		Name:      "gate",
		Condition: "true",
	})
	_, badCondition := f.releases.PutGate(ctx, Gate{
		Name:        "gate",
		Environment: "PROD",
		Condition:   "release.name",
	})

	for _, tt := range []struct {
		name string
		err  error
		want error
	}{
		{
			name: "get",
			err:  getErr,
			want: ErrGateNotFound,
		},
		{
			name: "delete",
			err:  deleteErr,
			want: ErrGateNotFound,
		},
		{
			name: "no environment",
			err:  noEnvironment,
			want: ErrInvalidGate,
		},
		{
			name: "bad condition",
			err:  badCondition,
			want: ErrInvalidGate,
		},
	} {
		if !errors.Is(tt.err, tt.want) || strings.Contains(tt.err.Error(), "release") {
			t.Errorf("%s: err = %v, want %v without calling the gate a release", tt.name, tt.err, tt.want)
		}
	}
}

func TestGateReportsAnUnreadableManifest(t *testing.T) {
	t.Parallel()

	f := newReleaseFixture(t)
	b := f.create(t, "1.0", false, artifactSource("libs/app.bin"))

	if _, err := f.releases.PutGate(t.Context(), Gate{
		Name:        "signed",
		Environment: "PROD",
		Condition:   "signed",
	}); err != nil {
		t.Fatal(err)
	}

	manifest, err := f.repos.Stat(t.Context(), b.Repository, f.releases.ManifestPath(b))
	if err != nil {
		t.Fatal(err)
	}

	if err := f.blobs.Delete(t.Context(), objectstore.Key(manifest.Checksums.SHA256)); err != nil {
		t.Fatal(err)
	}

	_, err = f.releases.Promote(t.Context(), "", "app", "1.0", PromotionSpec{
		Environment: "PROD",
	}, "admin", Access{})
	if err == nil || errors.Is(err, ErrRefused) {
		t.Errorf("Promote with an unreadable manifest = %v; want an error other than ErrRefused", err)
	}
}

func TestReleaseSourcedFromAReleaseChecksItsFiles(t *testing.T) {
	t.Parallel()

	f := newReleaseFixture(t)
	b := f.create(t, "1.0", false, artifactSource("libs/app.bin"))
	f.deploy(t, b.Repository, b.artifactPath(b.Artifacts[0]), "replaced")

	_, err := f.releases.Create(t.Context(), "", Spec{
		Name:    "app",
		Version: "2.0",
		Sources: []Source{
			{
				Kind: SourceReleases,
				Releases: []Ref{
					{
						Name:    "app",
						Version: "1.0",
					},
				},
			},
		},
	}, "admin", Access{})
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("Create from a release whose file changed = %v; want ErrInvalid", err)
	}
}

func TestFailedUpdateRemovesTheFilesItCopied(t *testing.T) {
	t.Parallel()

	f := newReleaseFixture(t)
	b := f.create(t, "1.0", true, artifactSource("libs/app.bin"))
	f.deploy(t, "libs", "app.bin", "changed")

	_, err := f.releases.Update(t.Context(), "", Spec{
		Name:    "app",
		Version: "1.0",
		Sources: artifactSource("libs/other.bin", "libs/app.bin"),
	}, "admin", Access{})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("Update with a changed artifact = %v; want ErrInvalid", err)
	}

	copied := b.artifactPath(Artifact{
		Path: "other.bin",
	})
	if _, err := f.repos.Stat(t.Context(), b.Repository, copied); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("Stat of %s after the failed update = %v; want ErrNotFound", copied, err)
	}
}

func TestPromoteOverwritesOnlyWithDeletePermission(t *testing.T) {
	t.Parallel()

	writeOnly := Access{
		CanWrite: func(string, string) bool {
			return true
		},
		CanDelete: func(string, string) bool {
			return false
		},
	}

	tests := []struct {
		name     string
		existing string
		access   Access
		wantErr  error
		want     string
	}{
		{
			name:     "write only over other content",
			existing: "deployed by hand",
			access:   writeOnly,
			wantErr:  ErrForbidden,
			want:     "deployed by hand",
		},
		{
			name:     "write only over the same content",
			existing: "application",
			access:   writeOnly,
			want:     "application",
		},
		{
			name:   "write only to a free path",
			access: writeOnly,
			want:   "application",
		},
		{
			name:     "delete over other content",
			existing: "deployed by hand",
			access:   Access{},
			want:     "application",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newReleaseFixture(t)
			f.create(t, "1.0", false, artifactSource("libs/app.bin"))

			if tt.existing != "" {
				f.deploy(t, "prod", "app.bin", tt.existing)
			}

			_, err := f.releases.Promote(t.Context(), "", "app", "1.0", PromotionSpec{
				Environment: "PROD",
			}, "dev", tt.access)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Promote = %v; want %v", err, tt.wantErr)
			}

			got, err := f.repos.Stat(t.Context(), "prod", "app.bin")
			if err != nil {
				t.Fatal(err)
			}

			want := sha256.Sum256([]byte(tt.want))
			if got.Checksums.SHA256 != hex.EncodeToString(want[:]) {
				t.Errorf("prod/app.bin holds other content than %q", tt.want)
			}
		})
	}
}

func TestAChangedReleaseFileIsNeitherPromotedNorExported(t *testing.T) {
	t.Parallel()

	f := newReleaseFixture(t)
	b := f.create(t, "1.0", false, artifactSource("libs/app.bin"))
	f.deploy(t, b.Repository, b.artifactPath(b.Artifacts[0]), "replaced")

	_, err := f.releases.Promote(t.Context(), "", "app", "1.0", PromotionSpec{
		Environment: "PROD",
	}, "admin", Access{})
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("Promote of a release whose file changed = %v; want ErrInvalid", err)
	}

	if _, err := f.repos.Stat(t.Context(), "prod", "app.bin"); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("Stat of prod/app.bin after the refused promotion = %v; want ErrNotFound", err)
	}

	if _, err := f.releases.Export(t.Context(), "", "app", "1.0", "admin"); !errors.Is(err, ErrInvalid) {
		t.Errorf("Export of a release whose file changed = %v; want ErrInvalid", err)
	}

	signed, err := f.releases.signed(t.Context(), b)
	if err != nil {
		t.Fatal(err)
	}

	if signed {
		t.Error("signed of a release whose file changed = true; want false")
	}
}

func TestSignedNeedsTheManifestOfTheVersion(t *testing.T) {
	t.Parallel()

	f := newReleaseFixture(t)
	b := f.create(t, "1.0", false, artifactSource("libs/app.bin"))
	other := f.create(t, "2.0", false, artifactSource("libs/other.bin"))

	manifest, err := f.repos.Stat(t.Context(), other.Repository, f.releases.ManifestPath(other))
	if err != nil {
		t.Fatal(err)
	}

	if err := f.repos.Copy(t.Context(), repository.CopyInput{
		Source:                manifest,
		DestinationRepository: b.Repository,
		DestinationPath:       f.releases.ManifestPath(b),
	}); err != nil {
		t.Fatal(err)
	}

	signed, err := f.releases.signed(t.Context(), b)
	if err != nil {
		t.Fatal(err)
	}

	if signed {
		t.Error("signed with another version's manifest = true; want false")
	}
}

func TestPromoteCopiesNothingWhenATargetIsRefused(t *testing.T) {
	t.Parallel()

	f := newReleaseFixture(t)
	f.create(t, "1.0", false, artifactSource("libs/app.bin", "libs/other.bin"))
	f.deploy(t, "prod", "other.bin", "original")

	tests := []struct {
		name   string
		access Access
	}{
		{
			name: "no write permission on a later target",
			access: Access{
				CanWrite: func(_, path string) bool {
					return path != "other.bin"
				},
			},
		},
		{
			name: "no delete permission to overwrite a later target",
			access: Access{
				CanDelete: func(string, string) bool {
					return false
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.releases.Promote(t.Context(), "", "app", "1.0", PromotionSpec{
				Environment: "PROD",
			}, "dev", tt.access)
			if !errors.Is(err, ErrForbidden) {
				t.Fatalf("Promote = %v; want ErrForbidden", err)
			}

			if _, err := f.repos.Stat(t.Context(), "prod", "app.bin"); !errors.Is(err, repository.ErrNotFound) {
				t.Errorf("Stat of prod/app.bin after the refused promotion = %v; want ErrNotFound", err)
			}
		})
	}
}

func TestBlockedFilesStayOutOfReleases(t *testing.T) {
	t.Parallel()

	f := newReleaseFixture(t)
	f.create(t, "1.0", false, artifactSource("libs/app.bin"))

	blocked := map[string]bool{}

	f.releases.SetDownloadBlocker(func(_ context.Context, file repository.Item) error {
		if blocked[file.Name] {
			return fmt.Errorf("%w: %s is blocked", repository.ErrForbidden, file.Name)
		}

		return nil
	})

	blocked["other.bin"] = true

	if _, err := f.releases.Create(t.Context(), "", Spec{
		Name:    "app",
		Version: "2.0",
		Sources: artifactSource("libs/other.bin"),
	}, "admin", Access{}); !errors.Is(err, repository.ErrForbidden) {
		t.Errorf("Create with a blocked file = %v; want ErrForbidden", err)
	}

	blocked["app.bin"] = true

	if _, err := f.releases.Export(t.Context(), "", "app", "1.0", "admin"); !errors.Is(err, repository.ErrForbidden) {
		t.Errorf("Export of a release holding a blocked file = %v; want ErrForbidden", err)
	}

	if _, err := f.releases.Promote(t.Context(), "", "app", "1.0", PromotionSpec{
		Environment: "PROD",
	}, "admin", Access{}); !errors.Is(err, repository.ErrForbidden) {
		t.Errorf("Promote of a release holding a blocked file = %v; want ErrForbidden", err)
	}

	if _, err := f.repos.Stat(t.Context(), "prod", "app.bin"); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("the blocked file was promoted: %v", err)
	}
}
