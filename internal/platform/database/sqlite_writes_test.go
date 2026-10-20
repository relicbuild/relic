package database_test

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"relic/internal/platform/database"
)

func TestSQLiteWritesTakeTurnsWithABulkImport(t *testing.T) {
	t.Parallel()

	db, err := database.Open(t.Context(), "sqlite://"+filepath.Join(t.TempDir(), "relic.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(db.Close)

	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}

	ctx, stop := context.WithCancel(t.Context())

	var importer sync.WaitGroup

	importer.Go(func() {
		for batch := 0; ctx.Err() == nil; batch++ {
			_ = db.WithTx(ctx, func(q database.Querier) error {
				for i := range 1000 {
					p := maliciousPackage(fmt.Sprintf("MAL-%d-%d", batch, i))
					if err := q.UpsertMaliciousPackage(ctx, p); err != nil {
						return err
					}
				}

				return nil
			})
		}
	})

	t.Cleanup(func() {
		stop()
		importer.Wait()
	})

	time.Sleep(200 * time.Millisecond)

	var slowest time.Duration

	for i := range 20 {
		start := time.Now()

		if err := db.UpsertMaliciousPackage(t.Context(), maliciousPackage(fmt.Sprintf("MAL-user-%d", i))); err != nil {
			t.Fatalf("write %d during the import: %v", i, err)
		}

		slowest = max(slowest, time.Since(start))

		time.Sleep(50 * time.Millisecond)
	}

	if slowest > 2*time.Second {
		t.Errorf("a write waited %s for the bulk import; it should wait for one of its transactions at most", slowest)
	}
}

func maliciousPackage(id string) database.UpsertMaliciousPackageParams {
	return database.UpsertMaliciousPackageParams{
		ID:          id,
		Ecosystem:   "npm",
		Name:        id,
		AllVersions: true,
		Versions: []string{
			"1.0.0",
		},
		Ranges:   []byte(`[]`),
		Summary:  "malicious",
		Modified: time.Now(),
	}
}
