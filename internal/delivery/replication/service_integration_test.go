//go:build integration

package replication_test

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"relic/internal/artifacts/repository"
	"relic/internal/delivery/replication"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
	"relic/internal/platform/sealing"
)

func TestMain(m *testing.M) { databasetest.Main(m) }

func TestReplicationService(t *testing.T) {
	t.Parallel()
	db := databasetest.NewDB(t)

	box, err := sealing.New(bytes.Repeat([]byte{
		3,
	}, 32))
	if err != nil {
		t.Fatal(err)
	}

	s := replication.NewService(db, box)

	ctx := t.Context()
	if err := repository.NewService(db, objectstoretest.NewMemory(), slog.New(slog.DiscardHandler)).
		CreateRepository(ctx, repository.Repository{
			Name:  "libs",
			Class: repository.ClassLocal,
		}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Get(ctx, "libs"); !errors.Is(err, replication.ErrNotConfigured) ||
		strings.Contains(err.Error(), "push") {
		t.Fatalf("get before save = %v, want a not configured error naming no kind of replication", err)
	}

	cfg := replication.Config{
		Repository: "libs",
		TargetURL:  "https://target.example/libs",
		Username:   "u",
		Password:   "s3cret",
		Schedule:   "0 12 * * *",
	}
	if err := s.Save(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	row, err := db.GetReplication(ctx, "libs")
	if err != nil || bytes.Contains(row.Config, []byte("s3cret")) {
		t.Fatalf("stored = %s %v, want the password sealed", row.Config, err)
	}

	got, err := s.Get(ctx, "libs")
	if err != nil || got.Password != "" || !got.HasPassword || got.TargetURL != cfg.TargetURL {
		t.Fatalf("get = %+v %v", got, err)
	}

	cfg.Password, cfg.SyncDeletes = "", true
	if err := s.Update(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	loaded, _, err := replication.NewPusher(db, nil, nil, nil).Load(ctx, "libs")
	if err != nil {
		t.Fatal(err)
	}

	if plain, err := box.OpenString(
		loaded.Password,
		replication.PasswordPlace("libs"),
	); err != nil || plain != "s3cret" ||
		!loaded.SyncDeletes {
		t.Errorf("after update: password %q %v, config %+v", plain, err, loaded)
	}

	for i := range 50 {
		password := fmt.Sprintf("p%d", i)
		kept := cfg

		var wg sync.WaitGroup

		wg.Go(func() {
			if err := s.Save(ctx, kept); err != nil {
				t.Error(err)
			}
		})
		wg.Go(func() {
			changed := kept
			changed.Password = password

			if err := s.Save(ctx, changed); err != nil {
				t.Error(err)
			}
		})
		wg.Wait()

		loaded, _, err := replication.NewPusher(db, nil, nil, nil).Load(ctx, "libs")
		if err != nil {
			t.Fatal(err)
		}

		if plain, _ := box.OpenString(loaded.Password, replication.PasswordPlace("libs")); plain != password {
			t.Fatalf("a save that keeps the password, concurrent with one setting %q, left %q", password, plain)
		}
	}

	list, err := s.List(ctx)
	if err != nil || len(list) != 1 || list[0].Repository != "libs" {
		t.Errorf("list = %+v %v", list, err)
	}

	deleted := make(chan error, 1)

	if err := db.WithLock(ctx, "replication:libs", func() error {
		go func() {
			deleted <- s.Delete(ctx, "libs")
		}()

		select {
		case err := <-deleted:
			deleted <- err

			return errors.New("a delete did not wait for a save of the same repository")
		case <-time.After(300 * time.Millisecond):
			return nil
		}
	}); err != nil {
		t.Fatal(err)
	}

	if err := <-deleted; err != nil {
		t.Fatal(err)
	}

	if err := s.Delete(ctx, "libs"); !errors.Is(err, replication.ErrNotConfigured) {
		t.Errorf("second delete = %v", err)
	}

	if err := s.Update(ctx, cfg); !errors.Is(err, replication.ErrNotConfigured) {
		t.Errorf("update after delete = %v", err)
	}

	if _, err := s.Get(ctx, "libs"); !errors.Is(err, replication.ErrNotConfigured) {
		t.Errorf("an update after a delete recreated the replication: %v", err)
	}
}
