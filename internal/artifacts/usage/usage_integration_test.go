//go:build integration

package usage

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"relic/internal/artifacts/repository"
	"relic/internal/platform/database"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/events"
	"relic/internal/platform/objectstore/objectstoretest"
)

func TestMain(m *testing.M) { databasetest.Main(m) }

func TestSnapshotAcrossMidnightKeepsTheLastRetainedDay(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := databasetest.NewDB(t)
	repos := repository.NewService(db, objectstoretest.NewMemory(), slog.New(slog.DiscardHandler))

	if err := repos.CreateRepository(ctx, repository.Repository{
		Name:   "libs",
		Class:  repository.ClassLocal,
		Format: repository.FormatGeneric,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := repos.Deploy(ctx, repository.DeployInput{
		Repository: "libs",
		Path:       "a.bin",
		Body:       strings.NewReader("1234"),
		User:       "admin",
	}); err != nil {
		t.Fatal(err)
	}

	svc := NewService(db, repos.Projects)
	svc.now = func() time.Time {
		return time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	}

	if err := svc.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}

	beforeMidnight := time.Date(2024, 6, 1, 23, 59, 59, 0, time.UTC).Add(Retention)
	reads := 0
	svc.now = func() time.Time {
		reads++
		if reads == 1 {
			return beforeMidnight
		}

		return beforeMidnight.Add(time.Second)
	}

	if err := svc.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}

	rows, err := db.ListRepositoryUsage(ctx, database.ListRepositoryUsageParams{
		FromDay: "2024-06-01",
		ToDay:   "2024-06-01",
	})
	if err != nil || len(rows) == 0 {
		t.Errorf("usage of the last retained day = %v, %v; want it kept", rows, err)
	}
}

func TestUsageReport(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := databasetest.NewDB(t)
	log := slog.New(slog.DiscardHandler)
	repos := repository.NewService(db, objectstoretest.NewMemory(), log)

	for name, project := range map[string]string{
		"libs":  "acme",
		"tools": "",
	} {
		if err := repos.CreateRepository(ctx, repository.Repository{
			Name:    name,
			Class:   repository.ClassLocal,
			Format:  repository.FormatGeneric,
			Project: project,
		}); err != nil {
			t.Fatal(err)
		}
	}

	deploy := func(repo, path, body string) {
		t.Helper()

		if _, err := repos.Deploy(ctx, repository.DeployInput{
			Repository: repo,
			Path:       path,
			Body:       strings.NewReader(body),
			User:       "admin",
		}); err != nil {
			t.Fatal(err)
		}
	}

	today := time.Now()
	twoDaysAgo := today.AddDate(0, 0, -2)
	svc := NewService(db, repos.Projects)
	svc.now = func() time.Time {
		return twoDaysAgo
	}

	deploy("libs", "a.bin", "1234")

	if err := svc.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}

	deploy("libs", "b.bin", "123456")
	deploy("tools", "t.bin", "12")

	svc.now = func() time.Time {
		return today
	}

	if err := svc.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}

	rec := NewRecorder(db, log)
	rec.now = func() time.Time {
		return today
	}

	for range 3 {
		rec.Download("libs", "alice")
	}

	rec.Download("tools", "bob")
	rec.Download("tools", "")
	rec.Publish(ctx, events.Event{
		Type:  events.ArtifactDeployed,
		Actor: "carol",
		Data: map[string]any{
			"repository": "libs",
		},
	})
	rec.Publish(ctx, events.Event{
		Type:  events.ArtifactDeployed,
		Actor: repository.SystemUser,
		Data: map[string]any{
			"repository": "libs",
		},
	})

	cancelled, cancel := context.WithCancel(ctx)
	cancel()

	if err := rec.Flush(cancelled); err == nil {
		t.Error("a flush that could not write reported success")
	}

	if err := rec.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	rep, err := svc.Report(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}

	byRepo := map[string]RepositoryUsage{}
	for _, r := range rep.Repositories {
		byRepo[r.Repository] = r
	}

	libs := byRepo["libs"]
	if libs.Files != 2 || libs.Bytes != 10 || libs.Growth != 6 || libs.Downloads != 3 || libs.Deploys != 1 ||
		libs.Project != "acme" {
		t.Errorf("libs = %+v; want 2 files, 10 bytes, 6 grown, 3 downloads, 1 deploy, project acme", libs)
	}

	if tools := byRepo["tools"]; tools.Downloads != 2 || tools.Bytes != 2 {
		t.Errorf("tools = %+v", tools)
	}

	if len(rep.Projects) != 1 || rep.Projects[0].Project != "acme" || rep.Projects[0].Bytes != 10 {
		t.Errorf("projects = %+v", rep.Projects)
	}

	if len(rep.TopDownloaders) != 2 || rep.TopDownloaders[0].User != "alice" || rep.TopDownloaders[0].Downloads != 3 {
		t.Errorf("top downloaders = %+v", rep.TopDownloaders)
	}

	if len(rep.TopDeployers) != 1 || rep.TopDeployers[0].User != "carol" {
		t.Errorf("top deployers = %+v", rep.TopDeployers)
	}

	if len(rep.Days) != 2 || rep.Days[0].Bytes != 4 || rep.Days[1].Bytes != 12 {
		t.Errorf("days = %+v", rep.Days)
	}
}
