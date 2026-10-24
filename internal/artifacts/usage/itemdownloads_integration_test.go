//go:build integration

package usage_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"relic/internal/artifacts/repository"
	"relic/internal/artifacts/usage"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
)

func TestFlushKeepsAFailedBatch(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := databasetest.NewDB(t)
	log := slog.New(slog.DiscardHandler)
	repos := repository.NewService(db, objectstoretest.NewMemory(), log)

	if err := repos.CreateRepository(ctx, repository.Repository{
		Name:   "libs",
		Class:  repository.ClassLocal,
		Format: repository.FormatGeneric,
	}); err != nil {
		t.Fatal(err)
	}

	item, err := repos.Deploy(ctx, repository.DeployInput{
		Repository: "libs",
		Path:       "a.bin",
		Body:       strings.NewReader("a"),
		User:       "admin",
	})
	if err != nil {
		t.Fatal(err)
	}

	rec := usage.NewItemDownloads(db, log)
	rec.Download(item.ID, "libs", "alice")
	rec.Download(item.ID, "libs", "bob")

	cancelled, cancel := context.WithCancel(ctx)
	cancel()

	if err := rec.Flush(cancelled); err == nil {
		t.Fatal("a flush that could not write reported success")
	}

	rec.Download(item.ID, "libs", "carol")

	if err := rec.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	totals, err := rec.Of(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}

	if totals.Count != 3 || totals.LastDownloadedBy != "carol" {
		t.Errorf("totals = %+v; want 3 downloads, last by carol", totals)
	}
}
