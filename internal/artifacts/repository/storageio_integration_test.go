//go:build integration

package repository

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"relic/internal/platform/database"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore"
	"relic/internal/platform/objectstore/objectstoretest"
)

// txWatchingStore fails the test when an object store call runs while one
// of the database's transactions is open.
type txWatchingStore struct {
	objectstore.Store

	t  *testing.T
	db *database.DB

	mu    sync.Mutex
	calls []string
}

func (s *txWatchingStore) check(ctx context.Context, op, key string) {
	var open int

	err := s.db.Pool().QueryRow(
		context.WithoutCancel(ctx),
		`SELECT count(*) FROM pg_stat_activity
		 WHERE datname = current_database() AND state LIKE 'idle in transaction%'`,
	).Scan(&open)
	if err != nil {
		s.t.Errorf("check transactions: %v", err)
		return
	}

	if open > 0 {
		s.mu.Lock()
		s.calls = append(s.calls, op+" "+key)
		s.mu.Unlock()
	}
}

func (s *txWatchingStore) Put(ctx context.Context, key string, r io.Reader, size int64) error {
	s.check(ctx, "put", key)

	return s.Store.Put(ctx, key, r, size)
}

func (s *txWatchingStore) Head(ctx context.Context, key string) (objectstore.Info, error) {
	s.check(ctx, "head", key)

	return s.Store.Head(ctx, key)
}

func (s *txWatchingStore) Copy(ctx context.Context, srcKey, dstKey string) error {
	s.check(ctx, "copy", dstKey)

	return s.Store.Copy(ctx, srcKey, dstKey)
}

func (s *txWatchingStore) Delete(ctx context.Context, key string) error {
	s.check(ctx, "delete", key)

	return s.Store.Delete(ctx, key)
}

func TestStorageIOOutsideTransactions(t *testing.T) {
	t.Parallel()
	databasetest.RequirePostgreSQL(t)

	ctx := t.Context()
	db := databasetest.NewDB(t)
	store := &txWatchingStore{
		Store: objectstoretest.NewMemory(),
		t:     t,
		db:    db,
	}

	s := NewService(db, store, slog.New(slog.DiscardHandler))
	if err := s.CreateRepository(ctx, Repository{
		Name:   "generic-local",
		Class:  ClassLocal,
		Format: "generic",
	}); err != nil {
		t.Fatal(err)
	}

	deploy(t, s, "a.txt", "alpha")
	deploy(t, s, "a.txt", "alpha two")
	deploy(t, s, "b.txt", "beta")

	if err := s.Delete(ctx, "generic-local", "b.txt"); err != nil {
		t.Fatal(err)
	}

	s.now = func() time.Time {
		return time.Now().UTC().Add(2 * time.Hour)
	}

	if n, err := s.CollectGarbage(ctx, time.Hour, 10); err != nil || n != 2 {
		t.Fatalf("CollectGarbage = %d, %v; want 2", n, err)
	}

	if len(store.calls) > 0 {
		t.Errorf("object store calls inside transactions: %v", store.calls)
	}
}

func TestDeployTakesBackABlobBeingCollected(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		// objectDeletedFirst deletes the object before the deploy takes
		// the blob back.
		objectDeletedFirst bool
	}{
		{
			name:               "taken back before its object is deleted",
			objectDeletedFirst: false,
		},
		{
			name:               "taken back after its object is deleted",
			objectDeletedFirst: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, blobs, db := newTestService(t)
			ctx := t.Context()

			deploy(t, s, "old.txt", "recycled")

			if err := s.Delete(ctx, "generic-local", "old.txt"); err != nil {
				t.Fatal(err)
			}

			marked, err := db.MarkCollectableBlobs(ctx, database.MarkCollectableBlobsParams{
				Cutoff: time.Now().Add(time.Hour),
				Max:    10,
			})
			if err != nil || len(marked) != 1 {
				t.Fatalf("mark = %v, %v", marked, err)
			}

			if tt.objectDeletedFirst {
				if err := s.deleteObject(ctx, marked[0]); err != nil {
					t.Fatal(err)
				}
			}

			item := deploy(t, s, "new.txt", "recycled")

			if !tt.objectDeletedFirst {
				if err := s.deleteObject(ctx, marked[0]); err != nil {
					t.Fatal(err)
				}
			}

			if n, err := db.DeleteDeletingBlobs(ctx, marked); err != nil || n != 0 {
				t.Errorf("deleted %d rows, %v; want the taken-back row kept", n, err)
			}

			rc, err := s.Open(ctx, item, nil)
			if err != nil {
				t.Fatalf("the redeployed content is unreadable: %v", err)
			}

			body, _ := io.ReadAll(rc)
			_ = rc.Close()

			if string(body) != "recycled" || !strings.Contains(strings.Join(blobs.Keys(), ","), marked[0]) {
				t.Errorf("content = %q, objects = %v", body, blobs.Keys())
			}

			if n := refcount(t, db, "recycled"); n != 1 {
				t.Errorf("refcount = %d, want 1", n)
			}
		})
	}
}
