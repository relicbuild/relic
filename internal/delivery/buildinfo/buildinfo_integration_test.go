//go:build integration

package buildinfo_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
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

func deployFile(t *testing.T, repos *repository.Service, repo, path, content string) repository.Item {
	t.Helper()

	if _, err := repos.GetRepository(t.Context(), repo); errors.Is(err, repository.ErrRepositoryNotFound) {
		err := repos.CreateRepository(t.Context(), repository.Repository{
			Name:   repo,
			Class:  repository.ClassLocal,
			Format: repository.FormatGeneric,
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	it, err := repos.Deploy(t.Context(), repository.DeployInput{
		Repository: repo,
		Path:       path,
		Body:       strings.NewReader(content),
		User:       "admin",
	})
	if err != nil {
		t.Fatal(err)
	}

	return it
}

func producing(number string, started time.Time, path string, file repository.Item) buildinfo.Document {
	return buildinfo.Document{
		Name:    "app",
		Number:  number,
		Started: started,
		Modules: []buildinfo.Module{
			{
				ID: "app",
				Artifacts: []buildinfo.Artifact{
					{
						Name: file.Name,
						Path: path,
						SHA1: file.Checksums.SHA1,
					},
				},
			},
		},
	}
}

func TestPromoteNeedsDeleteToReplaceAFile(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	builds, repos, _ := newService(t)

	built := deployFile(t, repos, "src", "ow/f.txt", "new")
	deployFile(t, repos, "dst", "ow/f.txt", "original")
	same := deployFile(t, repos, "src", "ow/same.txt", "same")
	deployFile(t, repos, "dst", "ow/same.txt", "same")

	doc := producing("1", time.Now(), "src/ow/f.txt", built)
	doc.Modules[0].Artifacts = append(doc.Modules[0].Artifacts, buildinfo.Artifact{
		Name: same.Name,
		Path: "src/ow/same.txt",
		SHA1: same.Checksums.SHA1,
	})

	if err := builds.PublishDocument(ctx, "", doc, "admin"); err != nil {
		t.Fatal(err)
	}

	msgs, err := builds.Promote(ctx, "", "app", "1", buildinfo.Promotion{
		TargetRepository: "dst",
		Copy:             true,
	}, "dev", func(_ repository.Item, a auth.Action) bool {
		return a != auth.ActionDelete
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(msgs) != 1 || msgs[0].Level != buildinfo.LevelWarning ||
		!strings.Contains(msgs[0].Text, "skipped src/ow/f.txt: permission denied") {
		t.Errorf("messages = %+v, want one skipped src/ow/f.txt", msgs)
	}

	kept, err := repos.Stat(ctx, "dst", "ow/f.txt")
	if err != nil || kept.Checksums.SHA1 == built.Checksums.SHA1 {
		t.Errorf("dst/ow/f.txt = %+v %v, want the original kept", kept.Checksums, err)
	}
}

func TestDiscardDeletesOnlyTheRecordedArtifacts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		recordedPath string
		removeFirst  bool
	}{
		{
			name:         "path with the repository",
			recordedPath: "z-src/del/x.bin",
		},
		{
			name:         "path inside the repository",
			recordedPath: "del/x.bin",
		},
		{
			name:         "recorded file already gone",
			recordedPath: "z-src/del/x.bin",
			removeFirst:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			builds, repos, _ := newService(t)
			now := time.Now()

			deployFile(t, repos, "a-other", "other/innocent.bin", "shared content")
			built := deployFile(t, repos, "z-src", "del/x.bin", "shared content")
			next := deployFile(t, repos, "z-src", "del/y.bin", "run 2")

			for _, doc := range []buildinfo.Document{
				producing("1", now.Add(-time.Hour), tt.recordedPath, built),
				producing("2", now, "z-src/del/y.bin", next),
			} {
				if err := builds.PublishDocument(ctx, "", doc, "admin"); err != nil {
					t.Fatal(err)
				}
			}

			if tt.removeFirst {
				if err := repos.Delete(ctx, "z-src", "del/x.bin"); err != nil {
					t.Fatal(err)
				}
			}

			err := builds.Discard(ctx, "", "app", buildinfo.Retention{
				MaxRuns:         1,
				DeleteArtifacts: true,
			}, buildinfo.AllowAll)
			if err != nil {
				t.Fatal(err)
			}

			if _, err := repos.Stat(ctx, "a-other", "other/innocent.bin"); err != nil {
				t.Errorf("unrelated file with the same content: %v, want it kept", err)
			}

			if _, err := repos.Stat(ctx, "z-src", "del/x.bin"); !errors.Is(err, repository.ErrNotFound) {
				t.Errorf("artifact of the discarded run: %v, want it deleted", err)
			}

			if _, err := repos.Stat(ctx, "z-src", "del/y.bin"); err != nil {
				t.Errorf("artifact of the kept run: %v", err)
			}
		})
	}
}
