//go:build integration

package replication_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"relic/internal/artifacts/repository"
	"relic/internal/delivery/replication"
	"relic/internal/platform/database"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/events"
	"relic/internal/platform/objectstore/objectstoretest"
)

func TestChangesQueueRunsAndKeepTombstones(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := databasetest.NewDB(t)
	log := slog.New(slog.DiscardHandler)
	repos := repository.NewService(db, objectstoretest.NewMemory(), log)

	for _, name := range []string{
		"fed",
		"pushed",
		"pulled",
		"plain",
		"unreadable",
	} {
		if err := repos.CreateRepository(ctx, repository.Repository{
			Name:  name,
			Class: repository.ClassLocal,
		}); err != nil {
			t.Fatal(err)
		}
	}

	svc := replication.NewService(db, nil)

	for repo, cfg := range map[string]replication.Config{
		"fed": {
			Direction: replication.DirectionFederated,
		},
		"pushed": {
			OnChange: true,
		},
		"pulled": {
			Direction: replication.DirectionPull,
			OnChange:  true,
		},
	} {
		cfg.Repository, cfg.TargetURL, cfg.Enabled, cfg.Schedule = repo, "https://peer.example/"+repo, true, "@daily"
		if err := svc.Save(ctx, cfg); err != nil {
			t.Fatal(err)
		}
	}

	var queued []string

	changes := replication.NewChanges(
		replication.NewPusher(db, repos, nil, log),
		func(_ context.Context, repo string) error {
			queued = append(queued, repo)
			return nil
		},
		log,
	)

	publish := func(eventType, repo, path, actor string) {
		changes.Publish(ctx, events.Event{
			Type:  eventType,
			Actor: actor,
			Time:  time.Now(),
			Data: map[string]any{
				"repository": repo,
				"path":       path,
			},
		})
	}

	publish(events.ArtifactDeployed, "fed", "a.txt", "alice")
	publish(events.ArtifactDeployed, "pushed", "a.txt", "alice")
	publish(events.ArtifactDeployed, "pulled", "a.txt", "alice")
	publish(events.ArtifactDeployed, "plain", "a.txt", "alice")
	publish(events.ArtifactDeployed, "fed", "b.txt", repository.SystemUser)
	publish(events.ArtifactDeleted, "fed", "gone.txt", "alice")

	if len(queued) != 3 || queued[0] != "fed" || queued[1] != "pushed" || queued[2] != "fed" {
		t.Errorf("queued = %v; want fed, pushed and fed again (not pulled, plain or replication's own deploys)", queued)
	}

	tombstones, err := db.ListTombstones(ctx, "fed")
	if err != nil || len(tombstones) != 1 || tombstones[0].Path != "gone.txt" {
		t.Fatalf("tombstones = %v, %v", tombstones, err)
	}

	publish(events.ArtifactDeployed, "fed", "gone.txt", "alice")

	if tombstones, _ := db.ListTombstones(ctx, "fed"); len(tombstones) != 0 {
		t.Errorf("a deploy kept the path's tombstone: %v", tombstones)
	}

	if err := db.UpsertReplication(ctx, database.UpsertReplicationParams{
		Repository: "unreadable",
		Config:     []byte(`{"enabled": "yes"}`),
	}); err != nil {
		t.Fatal(err)
	}

	publish(events.ArtifactDeleted, "unreadable", "gone.txt", "alice")

	if tombstones, err := db.ListTombstones(ctx, "unreadable"); err != nil || len(tombstones) != 1 {
		t.Errorf("a delete while the replication settings could not be read left tombstones %v %v", tombstones, err)
	}
}
