package database_test

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
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

	var (
		importer    sync.WaitGroup
		longestTurn time.Duration
	)

	importer.Go(func() {
		for batch := 0; ctx.Err() == nil; batch++ {
			start := time.Now()

			err := db.WithTx(ctx, func(q database.Querier) error {
				for i := range 1000 {
					p := maliciousPackage(fmt.Sprintf("MAL-%d-%d", batch, i))
					if err := q.UpsertMaliciousPackage(ctx, p); err != nil {
						return err
					}
				}

				return nil
			})
			if err == nil {
				longestTurn = max(longestTurn, time.Since(start))
			}
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

	stop()
	importer.Wait()

	if slowest > 2*longestTurn {
		t.Errorf(
			"a write waited %s for the bulk import, whose longest transaction took %s; it should wait for one at most",
			slowest,
			longestTurn,
		)
	}
}

func TestSQLiteDeletesAWithdrawnAdvisoryThroughAnIndex(t *testing.T) {
	t.Parallel()

	db, err := database.Open(t.Context(), "sqlite://"+filepath.Join(t.TempDir(), "relic.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(db.Close)

	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}

	rows, err := db.Query(t.Context(), "EXPLAIN QUERY PLAN DELETE FROM malicious_packages WHERE id = ?", "MAL-1")
	if err != nil {
		t.Fatal(err)
	}

	defer rows.Close()

	var plan []string

	for rows.Next() {
		var (
			id, parent, unused int
			step               string
		)

		if err := rows.Scan(&id, &parent, &unused, &step); err != nil {
			t.Fatal(err)
		}

		plan = append(plan, step)
	}

	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	if !slices.ContainsFunc(plan, func(step string) bool {
		return strings.Contains(step, "USING INDEX")
	}) {
		t.Errorf("deleting an advisory scans the table: %q", plan)
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
