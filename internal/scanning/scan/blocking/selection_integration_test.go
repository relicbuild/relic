//go:build integration

package blocking_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"relic/internal/artifacts/repository"
	"relic/internal/platform/database"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
	"relic/internal/scanning/malicious"
	"relic/internal/scanning/scan"
	"relic/internal/scanning/scan/blocking"
	"relic/internal/scanning/scan/policy"
)

type countingQuerier struct {
	database.Querier

	repositoryLookups atomic.Int64
	itemLookups       atomic.Int64
}

func (q *countingQuerier) GetRepository(ctx context.Context, key string) (database.Repository, error) {
	q.repositoryLookups.Add(1)

	return q.Querier.GetRepository(ctx, key)
}

func (q *countingQuerier) GetItem(ctx context.Context, arg database.GetItemParams) (database.Item, error) {
	q.itemLookups.Add(1)

	return q.Querier.GetItem(ctx, arg)
}

func (q *countingQuerier) ListItemsAt(ctx context.Context, arg database.ListItemsAtParams) ([]database.Item, error) {
	q.itemLookups.Add(1)

	return q.Querier.ListItemsAt(ctx, arg)
}

type gateFixture struct {
	db      *database.DB
	queries *countingQuerier
	repos   *repository.Service
	gate    repository.VersionGate
	repo    repository.Repository
}

func newGateFixture(t *testing.T) gateFixture {
	t.Helper()

	ctx := t.Context()
	db := databasetest.NewDB(t)
	queries := &countingQuerier{
		Querier: db.Querier,
	}
	db.Querier = queries

	repos := repository.NewService(db, objectstoretest.NewMemory(), slog.New(slog.DiscardHandler))

	repo := repository.Repository{
		Name:   "npm-local",
		Class:  "local",
		Format: "npm",
	}
	if err := repos.CreateRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}

	pol := policy.Policy{
		Name: "block",
		Kind: policy.KindSecurity,
		Rules: []policy.Rule{
			{
				Name:           "unscanned",
				MinSeverity:    scan.SeverityCritical,
				BlockDownload:  true,
				BlockUnscanned: true,
			},
		},
	}
	watch := policy.Watch{
		Name:   "w",
		Active: true,
		Scopes: []policy.Scope{
			{
				Kind: policy.ScopeRepository,
				Names: []string{
					repo.Name,
				},
			},
		},
		Policies: []string{
			pol.Name,
		},
	}
	policyDoc, _ := json.Marshal(pol)
	watchDoc, _ := json.Marshal(watch)

	if err := db.UpsertScanPolicy(ctx, database.UpsertScanPolicyParams{
		Name: pol.Name,
		Kind: string(pol.Kind),
		Doc:  policyDoc,
		Now:  time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.UpsertScanWatch(ctx, database.UpsertScanWatchParams{
		Name: watch.Name,
		Doc:  watchDoc,
		Now:  time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	b := blocking.New(db)
	b.SetDecisions(&policy.Decisions{
		DB: db,
	})

	return gateFixture{
		db:      db,
		queries: queries,
		repos:   repos,
		gate:    b.VersionGate(malicious.NewCatalog(db), repos),
		repo:    repo,
	}
}

func (f gateFixture) deploy(t *testing.T, itemPath string) string {
	t.Helper()

	it, err := f.repos.Deploy(t.Context(), repository.DeployInput{
		Repository: f.repo.Name,
		Path:       itemPath,
		Body:       strings.NewReader(itemPath),
		User:       "admin",
	})
	if err != nil {
		t.Fatal(err)
	}

	return it.Checksums.SHA256
}

func (f gateFixture) markScanned(t *testing.T, sha256Hex, namePattern string) {
	t.Helper()

	ctx := t.Context()
	now := time.Now()

	if _, err := f.db.ClaimUnscanned(ctx, database.ClaimUnscannedParams{
		Now:         now,
		NamePattern: namePattern,
		MaxRows:     10,
	}); err != nil {
		t.Fatal(err)
	}

	if err := f.db.FinishScanArtifact(ctx, database.FinishScanArtifactParams{
		Sha256: sha256Hex,
		Status: scan.StatusDone,
		Now:    now,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestVersionGateRefusals(t *testing.T) {
	t.Parallel()

	f := newGateFixture(t)
	ctx := t.Context()

	f.deploy(t, "left-pad/-/left-pad-1.0.0.tgz")
	f.markScanned(t, f.deploy(t, "left-pad/-/left-pad-1.1.0.tgz"), `^left-pad-1\.1\.0\.tgz$`)
	f.deploy(t, "left-pad/-/left-pad-1.5.0.tgz")

	if err := f.db.UpsertMaliciousPackage(ctx, database.UpsertMaliciousPackageParams{
		ID:        "MAL-1",
		Ecosystem: "npm",
		Name:      malicious.Key("npm", "left-pad"),
		Versions: []string{
			"1.5.0",
			"1.6.0",
		},
		Ranges:   []byte("[]"),
		Summary:  "malicious",
		Modified: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		repository repository.Repository
		versions   []repository.Version
		want       map[string]bool
		wantErr    error
	}{
		{
			name:       "found, missing, folder, blocked and malicious",
			repository: f.repo,
			versions: []repository.Version{
				{
					Version: "1.0.0",
					Path:    "left-pad/-/left-pad-1.0.0.tgz",
				},
				{
					Version: "1.1.0",
					Path:    "left-pad/-/left-pad-1.1.0.tgz",
				},
				{
					Version: "1.2.0",
					Path:    "left-pad/-/left-pad-1.2.0.tgz",
				},
				{
					Version: "1.3.0",
					Path:    "left-pad/-",
				},
				{
					Version: "1.4.0",
				},
				{
					Version: "1.5.0",
					Path:    "left-pad/-/left-pad-1.5.0.tgz",
				},
				{
					Version: "1.6.0",
					Path:    "left-pad/-/left-pad-1.6.0.tgz",
				},
				{
					Version: "1.7.0",
					Path:    "/left-pad//-/left-pad-1.0.0.tgz",
				},
				{
					Version: "1.8.0",
					Path:    "/",
				},
			},
			want: map[string]bool{
				"1.0.0": true,
				"1.1.0": false,
				"1.5.0": true,
				"1.6.0": true,
				"1.7.0": true,
			},
		},
		{
			name:       "no versions",
			repository: f.repo,
			want:       map[string]bool{},
		},
		{
			name:       "invalid path",
			repository: f.repo,
			versions: []repository.Version{
				{
					Version: "1.0.0",
					Path:    "left-pad/-/left-pad-1.0.0.tgz",
				},
				{
					Version: "2.0.0",
					Path:    "left-pad/../x.tgz",
				},
			},
			wantErr: repository.ErrInvalidPath,
		},
		{
			name: "missing repository",
			repository: repository.Repository{
				Name:   "absent",
				Class:  "local",
				Format: "npm",
			},
			versions: []repository.Version{
				{
					Version: "1.0.0",
					Path:    "left-pad/-/left-pad-1.0.0.tgz",
				},
			},
			wantErr: repository.ErrRepositoryNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := f.gate(ctx, tt.repository, "npm", "left-pad", tt.versions)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}

			if tt.wantErr == nil && !maps.Equal(got, tt.want) {
				t.Errorf("refused = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestVersionGateLooksUpItemsOnce(t *testing.T) {
	t.Parallel()

	f := newGateFixture(t)

	versions := make([]repository.Version, 0, 25)

	for i := range 25 {
		version := fmt.Sprintf("1.%d.0", i)
		itemPath := "left-pad/-/left-pad-" + version + ".tgz"
		f.deploy(t, itemPath)
		versions = append(versions, repository.Version{
			Version: version,
			Path:    itemPath,
		})
	}

	f.queries.repositoryLookups.Store(0)
	f.queries.itemLookups.Store(0)

	refused, err := f.gate(t.Context(), f.repo, "npm", "left-pad", versions)
	if err != nil {
		t.Fatal(err)
	}

	if len(refused) != len(versions) {
		t.Errorf("refused %d versions, want %d", len(refused), len(versions))
	}

	if got := f.queries.repositoryLookups.Load(); got > 1 {
		t.Errorf("repository lookups = %d, want at most 1", got)
	}

	if got := f.queries.itemLookups.Load(); got != 1 {
		t.Errorf("item lookups = %d, want 1", got)
	}
}
