//go:build integration

package database_test

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"relic/internal/platform/database"
	"relic/internal/platform/database/databasetest"
)

func TestBackupWhileWriting(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	dir := t.TempDir()

	db, err := database.Open(ctx, "sqlite://"+filepath.Join(dir, "relic.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(db.Close)

	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	const groupsBeforeBackup = 20

	var wg sync.WaitGroup

	writing := make(chan struct{})

	wg.Go(func() {
		defer func() {
			select {
			case <-writing:
			default:
				close(writing)
			}
		}()

		for i := range 200 {
			if _, err := db.UpsertGroup(ctx, database.UpsertGroupParams{
				Name: fmt.Sprintf("group-%03d", i),
			}); err != nil {
				t.Errorf("write %d: %v", i, err)

				return
			}

			if i == groupsBeforeBackup-1 {
				close(writing)
			}
		}
	})

	<-writing

	backup := filepath.Join(dir, "backup.db")
	if err := database.Backup(ctx, db, backup); err != nil {
		t.Fatalf("backup: %v", err)
	}

	wg.Wait()

	if err := database.Backup(ctx, db, backup); err == nil {
		t.Error("a backup over an existing file succeeded")
	}

	snap, err := database.Open(ctx, "sqlite://"+backup)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(snap.Close)

	if err := snap.Migrate(ctx); err != nil {
		t.Fatalf("migrate the snapshot: %v", err)
	}

	groups, err := snap.ListGroups(ctx)
	if err != nil {
		t.Fatalf("read the snapshot: %v", err)
	}

	var written []string

	for _, g := range groups {
		if strings.HasPrefix(g.Name, "group-") {
			written = append(written, g.Name)
		}
	}

	slices.Sort(written)

	if len(written) < groupsBeforeBackup {
		t.Errorf(
			"the snapshot holds %d groups, want at least the %d written before it",
			len(written),
			groupsBeforeBackup,
		)
	}

	for i, name := range written {
		if name != fmt.Sprintf("group-%03d", i) {
			t.Fatalf("the snapshot's groups are not a prefix of the writes: %v", written)
		}
	}
}

func TestBackupRefusesPostgreSQL(t *testing.T) {
	t.Parallel()
	databasetest.RequirePostgreSQL(t)

	err := database.Backup(t.Context(), databasetest.NewDB(t), filepath.Join(t.TempDir(), "b.db"))
	if !errors.Is(err, database.ErrBackupNotSupported) {
		t.Errorf("Backup on PostgreSQL = %v, want ErrBackupNotSupported", err)
	}
}
