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
			errs <- db.Migrate(t.Context())
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
