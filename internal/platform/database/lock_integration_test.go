//go:build integration

package database_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"relic/internal/platform/database"
	"relic/internal/platform/database/databasetest"
)

func TestWithLockHoldersCanUseTheWholePool(t *testing.T) {
	t.Parallel()
	databasetest.RequirePostgreSQL(t)

	url := databasetest.NewDB(t).Pool().Config().ConnString()

	db, err := database.OpenWith(t.Context(), url, database.Options{
		MaxConnections: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(db.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	var wg sync.WaitGroup

	for i := range 4 {
		wg.Go(func() {
			err := db.WithLock(ctx, fmt.Sprintf("remote-fetch:%d", i), func() error {
				time.Sleep(10 * time.Millisecond)

				return db.Ping(ctx)
			})
			if err != nil {
				t.Error(err)
			}
		})
	}

	wg.Wait()
}

func TestWithLockExcludesOtherHolders(t *testing.T) {
	t.Parallel()

	db := databasetest.NewDB(t)
	handles := []*database.DB{
		db,
	}

	if !databasetest.SQLite() {
		other, err := database.OpenWith(t.Context(), db.Pool().Config().ConnString(), database.Options{})
		if err != nil {
			t.Fatal(err)
		}

		t.Cleanup(other.Close)

		handles = append(handles, other)
	}

	var (
		holders, most atomic.Int32
		wg            sync.WaitGroup
	)

	for i := range 8 {
		holder := handles[i%len(handles)]

		wg.Go(func() {
			err := holder.WithLock(t.Context(), "metadata:libs/org/example/lib", func() error {
				n := holders.Add(1)

				for {
					m := most.Load()
					if n <= m || most.CompareAndSwap(m, n) {
						break
					}
				}

				time.Sleep(5 * time.Millisecond)
				holders.Add(-1)

				return nil
			})
			if err != nil {
				t.Error(err)
			}
		})
	}

	wg.Wait()

	if got := most.Load(); got != 1 {
		t.Errorf("%d holders at once, want 1", got)
	}
}
