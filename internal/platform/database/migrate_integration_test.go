//go:build integration

package database_test

import (
	"math"
	"sync"
	"testing"

	"relic/internal/platform/database/databasetest"
)

func TestMain(m *testing.M) { databasetest.Main(m) }

func TestMigrateConcurrently(t *testing.T) {
	t.Parallel()
	db := databasetest.NewDBAt(t, math.MaxInt64)

	var wg sync.WaitGroup

	errs := make(chan error, 8)

	for range 8 {
		wg.Go(func() {
			_, err := db.Migrate(t.Context())
			errs <- err
		})
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Errorf("concurrent migrate: %v", err)
		}
	}
}

func TestMigrateReportsTheAppliedMigrations(t *testing.T) {
	t.Parallel()
	db := databasetest.NewDBAt(t, 1)

	applied, err := db.Migrate(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(applied) == 0 || applied[0].Version <= 1 || applied[0].Name == "" {
		t.Fatalf("applied = %+v, want the migrations after version 1", applied)
	}

	for i := 1; i < len(applied); i++ {
		if applied[i].Version <= applied[i-1].Version {
			t.Errorf("applied out of order: %+v", applied)
		}
	}

	again, err := db.Migrate(t.Context())
	if err != nil || len(again) != 0 {
		t.Errorf("migrating a migrated database = %+v, %v; want none applied", again, err)
	}
}
