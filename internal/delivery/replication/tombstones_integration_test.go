//go:build integration

package replication

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"relic/internal/artifacts/repository"
	"relic/internal/platform/database"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
)

type refusingTarget struct {
	Target

	refused string
	deleted []string
}

func (t *refusingTarget) Stat(context.Context, string) (RemoteFile, error) {
	return RemoteFile{
		Found:    true,
		Modified: time.Now().Add(-time.Hour),
	}, nil
}

func (t *refusingTarget) Delete(_ context.Context, path string) error {
	if path == t.refused {
		return errors.New("403 Forbidden")
	}

	t.deleted = append(t.deleted, path)

	return nil
}

func TestPushTombstonesGoesPastARefusedDelete(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := databasetest.NewDB(t)
	log := slog.New(slog.DiscardHandler)

	if err := repository.NewService(db, objectstoretest.NewMemory(), log).CreateRepository(ctx, repository.Repository{
		Name:  "fed",
		Class: repository.ClassLocal,
	}); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		"a.txt",
		"b.txt",
	} {
		if err := db.PutTombstone(ctx, database.PutTombstoneParams{
			RepoKey: "fed",
			Path:    path,
			Deleted: time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
	}

	target := &refusingTarget{
		refused: "a.txt",
	}
	pusher := NewPusher(db, nil, nil, log)

	n, err := pusher.pushTombstones(ctx, target, Config{
		Repository: "fed",
	})
	if err == nil {
		t.Error("pushTombstones hid the refused delete")
	}

	if n != 1 || len(target.deleted) != 1 || target.deleted[0] != "b.txt" {
		t.Errorf("pushTombstones deleted %d: %v; want b.txt", n, target.deleted)
	}
}
