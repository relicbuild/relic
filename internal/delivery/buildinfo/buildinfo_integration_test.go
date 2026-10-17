//go:build integration

package buildinfo_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"relic/internal/access/auth"
	"relic/internal/artifacts/repository"
	"relic/internal/delivery/buildinfo"
	"relic/internal/platform/database"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore"
)

func TestMain(m *testing.M) { databasetest.Main(m) }

type jsonFormat struct{}

func (jsonFormat) Parse(raw []byte) (buildinfo.Document, error) {
	var doc buildinfo.Document

	err := json.Unmarshal(raw, &doc)

	return doc, err
}

func (jsonFormat) Render(doc buildinfo.Document) ([]byte, error) {
	return json.Marshal(doc)
}

func (jsonFormat) Prepare(
	_ context.Context,
	raw []byte,
	_ func(ctx context.Context, sha1 string) (string, bool, error),
) ([]byte, error) {
	return raw, nil
}

func (jsonFormat) Path(name, number string, _ time.Time) string {
	return name + "/" + number + ".json"
}

func newService(t *testing.T) (*buildinfo.Service, *repository.Service, *database.DB) {
	t.Helper()

	db := databasetest.NewDB(t)

	blobs, err := objectstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	log := slog.New(slog.DiscardHandler)
	repos := repository.NewService(db, blobs, log)

	return buildinfo.NewService(db, repos, jsonFormat{}, log), repos, db
}

func run(number string, started time.Time) buildinfo.Document {
	return buildinfo.Document{
		Name:    "app",
		Number:  number,
		Started: started,
		Modules: []buildinfo.Module{
			{
				ID: "app:1",
				Dependencies: []buildinfo.Dependency{
					{
						ID:   "lib:1",
						SHA1: "da39a3ee5e6b4b0d3255bfef95601890afd80709",
					},
				},
			},
		},
	}
}

func TestPublishLeavesNoFileWhenTheRecordFails(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	builds, repos, db := newService(t)

	rows, err := db.Query(ctx, "DROP TABLE build_dependencies")
	if err != nil {
		t.Fatal(err)
	}

	rows.Close()

	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	if err := builds.PublishDocument(ctx, "", run("1", time.Now()), "admin"); err == nil {
		t.Fatal("PublishDocument succeeded without the dependencies table")
	}

	if _, err := repos.Stat(ctx, builds.Repository(""), "app/1.json"); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("Stat of the build-info file = %v; want ErrNotFound", err)
	}
}

func TestDeleteRemovesTheRunsItEmptiedBeforeAHeldOne(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	builds, repos, _ := newService(t)
	now := time.Now()

	for _, doc := range []buildinfo.Document{
		run("1", now.Add(-time.Hour)),
		run("2", now),
	} {
		if err := builds.PublishDocument(ctx, "", doc, "admin"); err != nil {
			t.Fatal(err)
		}
	}

	if err := repos.PutHold(ctx, builds.Repository(""), "app/1.json", "legal", "admin"); err != nil {
		t.Fatal(err)
	}

	err := builds.Delete(
		ctx,
		"",
		"app",
		nil,
		true,
		false,
		func(repository.Item, auth.Action) bool {
			return true
		},
	)
	if !errors.Is(err, repository.ErrHeld) {
		t.Fatalf("Delete = %v; want ErrHeld", err)
	}

	runs, err := builds.Runs(ctx, "", "app")
	if err != nil {
		t.Fatal(err)
	}

	if len(runs) != 1 || runs[0].Number != "1" {
		t.Errorf("runs after Delete = %+v; want only the held run 1", runs)
	}
}
