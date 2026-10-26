//go:build integration

package malicious

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"testing"
	"time"

	"relic/internal/platform/database"
	"relic/internal/platform/database/databasetest"
)

func TestStoreUpsertsABatch(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := databasetest.NewDB(t)
	r := &Refresher{
		db:  db,
		log: slog.New(slog.DiscardHandler),
	}
	modified := time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC)
	ranges := []Range{
		{
			Type: "SEMVER",
			Events: []Event{
				{
					Introduced: "0",
				},
				{
					Fixed: "2.0.0",
				},
			},
		},
	}

	pkg := func(id, name, summary string) Package {
		return Package{
			ID:        id,
			Ecosystem: "npm",
			Name:      name,
			Versions: []string{
				"1.0.0",
				"1.0.1",
			},
			Ranges:   ranges,
			Summary:  summary,
			Modified: modified,
		}
	}

	if err := r.store(ctx, []advisory{
		{
			id: "MAL-1",
			packages: []Package{
				pkg("MAL-1", "evil", "first"),
				pkg("MAL-1", "evil", "repeated in the same advisory"),
			},
		},
		{
			id: "MAL-2",
			packages: []Package{
				pkg("MAL-2", "gone", "withdrawn below"),
			},
		},
		{
			id:        "MAL-2",
			withdrawn: true,
		},
		{
			id: "MAL-3",
			packages: []Package{
				{
					ID:          "MAL-3",
					Ecosystem:   "npm",
					Name:        "every-version",
					AllVersions: true,
					Modified:    modified,
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	rows, err := db.ListMaliciousPackages(ctx, database.ListMaliciousPackagesParams{
		Ecosystems: []string{
			"npm",
		},
		Names: []string{
			"evil",
			"gone",
			"every-version",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(rows) != 2 {
		t.Fatalf("stored %d packages, want evil and every-version: %+v", len(rows), rows)
	}

	evil, all := rows[0], rows[1]

	var gotRanges []Range
	if err := json.Unmarshal(evil.Ranges, &gotRanges); err != nil {
		t.Fatal(err)
	}

	if evil.Name != "evil" || evil.AllVersions || evil.Summary != "repeated in the same advisory" ||
		!slices.Equal(evil.Versions, []string{
			"1.0.0",
			"1.0.1",
		}) ||
		len(gotRanges) != 1 || gotRanges[0].Events[1].Fixed != "2.0.0" || !evil.Modified.Equal(modified) {
		t.Errorf("evil = %+v, ranges %s", evil, evil.Ranges)
	}

	if all.Name != "every-version" || !all.AllVersions || len(all.Versions) != 0 {
		t.Errorf("every-version = %+v", all)
	}
}

func TestStoreWritesAFullBatchQuickly(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := databasetest.NewDB(t)
	r := &Refresher{
		db:  db,
		log: slog.New(slog.DiscardHandler),
	}

	batch := make([]advisory, 0, 2000)
	for i := range cap(batch) {
		id := fmt.Sprintf("MAL-%d", i)
		batch = append(batch, advisory{
			id: id,
			packages: []Package{
				{
					ID:        id,
					Ecosystem: "npm",
					Name:      fmt.Sprintf("package-%d", i),
					Versions: []string{
						"1.0.0",
					},
					Modified: time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC),
				},
			},
		})
	}

	start := time.Now()

	if err := r.store(ctx, batch); err != nil {
		t.Fatal(err)
	}

	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("storing %d advisories held the write transaction for %v", len(batch), elapsed)
	}

	stored, err := db.CountMaliciousPackages(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if stored != int64(len(batch)) {
		t.Errorf("stored %d packages, want %d", stored, len(batch))
	}
}
