//go:build integration

package buildinfo_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"relic/internal/access/auth"
	"relic/internal/access/project"
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

	return deployTagged(t, repos, repo, path, content, nil)
}

func deployTagged(
	t *testing.T,
	repos *repository.Service,
	repo, path, content string,
	props repository.Properties,
) repository.Item {
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
		Props:      props,
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

func TestPublishNeedsAnExistingProject(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	builds, repos, db := newService(t)

	err := builds.PublishDocument(ctx, "nope", run("1", time.Now()), "admin")
	if !errors.Is(err, buildinfo.ErrInvalid) {
		t.Errorf("publish to a missing project = %v, want ErrInvalid", err)
	}

	_, err = repos.GetRepository(ctx, builds.Repository("nope"))
	if !errors.Is(err, repository.ErrRepositoryNotFound) {
		t.Errorf("build-info repository of the missing project: %v, want none", err)
	}

	if err := db.UpsertProject(ctx, database.UpsertProjectParams{
		Key:    "acme",
		Config: []byte(`{}`),
		Now:    time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	if err := builds.PublishDocument(ctx, "acme", run("1", time.Now()), "admin"); err != nil {
		t.Errorf("publish to an existing project = %v", err)
	}
}

func TestRepublishReplacesTheRun(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	builds, repos, _ := newService(t)
	started := time.Now().Truncate(time.Millisecond)

	first := deployFile(t, repos, "src", "re/a1.txt", "a1")
	second := deployFile(t, repos, "src", "re/a2.txt", "a2")

	doc := producing("1", started, "src/re/a1.txt", first)
	if err := builds.PublishDocument(ctx, "", doc, "admin"); err != nil {
		t.Fatal(err)
	}

	if _, err := builds.Promote(ctx, "", "app", "1", buildinfo.Promotion{
		Status: "tested",
	}, "admin", buildinfo.AllowAll); err != nil {
		t.Fatal(err)
	}

	doc.Modules[0].Artifacts = append(doc.Modules[0].Artifacts, buildinfo.Artifact{
		Name: second.Name,
		Path: "src/re/a2.txt",
		SHA1: second.Checksums.SHA1,
	})

	for range 2 {
		if err := builds.PublishDocument(ctx, "", doc, "admin"); err != nil {
			t.Fatal(err)
		}
	}

	runs, err := builds.Runs(ctx, "", "app")
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %+v %v, want one", runs, err)
	}

	run, err := builds.Run(ctx, "", "app", "1")
	if err != nil {
		t.Fatal(err)
	}

	if got := len(run.Document.Modules[0].Artifacts); got != 2 {
		t.Errorf("artifacts of the republished run = %d, want 2", got)
	}

	if len(run.Statuses) != 1 || run.Statuses[0].Status != "tested" {
		t.Errorf("statuses = %+v, want the promotion of the replaced run", run.Statuses)
	}

	items, err := builds.Items(ctx, "", "app", "1", false)
	if err != nil || len(items) != 2 {
		t.Errorf("items = %+v %v, want both artifacts", items, err)
	}
}

func TestPromoteReportsEachFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		dryRun bool
		copy   bool
		want   string
	}{
		{
			name:   "dry run of a copy",
			dryRun: true,
			copy:   true,
			want:   "would copy src/p/f.txt to dst/p/f.txt",
		},
		{
			name:   "dry run of a move",
			dryRun: true,
			want:   "would move src/p/f.txt to dst/p/f.txt",
		},
		{
			name: "copy",
			copy: true,
			want: "copied src/p/f.txt to dst/p/f.txt",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			builds, repos, _ := newService(t)

			built := deployFile(t, repos, "src", "p/f.txt", "f")
			deployFile(t, repos, "dst", "keep.txt", "k")

			if err := builds.PublishDocument(
				ctx,
				"",
				producing("1", time.Now(), "src/p/f.txt", built),
				"admin",
			); err != nil {
				t.Fatal(err)
			}

			msgs, err := builds.Promote(ctx, "", "app", "1", buildinfo.Promotion{
				TargetRepository: "dst",
				Copy:             tt.copy,
				DryRun:           tt.dryRun,
			}, "admin", buildinfo.AllowAll)
			if err != nil {
				t.Fatal(err)
			}

			if len(msgs) != 1 || msgs[0].Level != buildinfo.LevelInfo || msgs[0].Text != tt.want {
				t.Errorf("messages = %+v, want info %q", msgs, tt.want)
			}

			_, err = repos.Stat(ctx, "dst", "p/f.txt")
			if tt.dryRun != errors.Is(err, repository.ErrNotFound) {
				t.Errorf("dst/p/f.txt after the promotion: %v", err)
			}
		})
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

	warnings := slices.DeleteFunc(msgs, func(m buildinfo.Message) bool {
		return m.Level != buildinfo.LevelWarning
	})
	if len(warnings) != 1 || !strings.Contains(warnings[0].Text, "skipped src/ow/f.txt: permission denied") {
		t.Errorf("warnings = %+v, want one skipped src/ow/f.txt", warnings)
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

func TestDiscardKeepsFilesOfAnotherRun(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		path  string
		props repository.Properties
	}{
		{
			name: "same build, another number, elsewhere",
			path: "del/x2.bin",
			props: repository.Properties{
				"build.name": {
					"app",
				},
				"build.number": {
					"2",
				},
			},
		},
		{
			name: "same build, another number, at the recorded path",
			path: "del/x.bin",
			props: repository.Properties{
				"build.name": {
					"app",
				},
				"build.number": {
					"2",
				},
			},
		},
		{
			name: "another build at the recorded path",
			path: "del/x.bin",
			props: repository.Properties{
				"build.name": {
					"other",
				},
				"build.number": {
					"1",
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			builds, repos, _ := newService(t)
			now := time.Now()

			gone := deployFile(t, repos, "z-src", "del/x.bin", "shared content")
			if err := repos.Delete(ctx, "z-src", "del/x.bin"); err != nil {
				t.Fatal(err)
			}

			kept := deployTagged(t, repos, "z-src", tt.path, "shared content", tt.props)

			for _, doc := range []buildinfo.Document{
				producing("1", now.Add(-time.Hour), "z-src/del/x.bin", gone),
				producing("2", now, "z-src/"+tt.path, kept),
			} {
				if err := builds.PublishDocument(ctx, "", doc, "admin"); err != nil {
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

			if _, err := repos.Stat(ctx, "z-src", tt.path); err != nil {
				t.Errorf("file of another run: %v, want it kept", err)
			}
		})
	}
}

func TestPublishKeepsBuildsOutOfAnotherProjectsRepository(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	builds, repos, db := newService(t)
	projects := project.New(db)

	for _, key := range []string{
		"alpha",
		"beta",
	} {
		if _, err := projects.Put(ctx, project.Project{
			Key:  key,
			Name: key,
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := repos.CreateRepository(ctx, repository.Repository{
		Name:    builds.Repository("beta"),
		Class:   repository.ClassLocal,
		Format:  repository.FormatBuild,
		Project: "alpha",
	}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		project string
	}{
		{
			name:    "repository of another project",
			project: "beta",
		},
		{
			name:    "project that does not exist",
			project: "gamma",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := builds.PublishDocument(ctx, tt.project, run("1", time.Now()), "admin")
			if !errors.Is(err, buildinfo.ErrInvalid) {
				t.Fatalf("PublishDocument = %v; want ErrInvalid", err)
			}

			if _, err := repos.Stat(ctx, builds.Repository(tt.project), "app/1.json"); err == nil {
				t.Error("the build-info file was stored")
			}
		})
	}
}
