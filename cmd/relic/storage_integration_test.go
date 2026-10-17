//go:build integration

package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"relic/api/adapters/artifactory/access"
	"relic/internal/access/auth"
	"relic/internal/app"
	"relic/internal/app/backgroundjobs"
	"relic/internal/app/config"
	"relic/internal/artifacts/repository"
	"relic/internal/delivery/replication"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore"
	"relic/internal/platform/objectstore/objectstoretest"
)

func TestStorageCheckCommand(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := databasetest.NewDB(t)
	log := slog.New(slog.DiscardHandler)
	blobs := objectstoretest.NewMemory()
	repos := repository.NewService(db, blobs, log)

	if err := app.BootstrapAdmin(ctx, db, "admin", "password", log); err != nil {
		t.Fatal(err)
	}

	tokens, err := auth.NewTokens(ctx, db, nil, access.Format{})
	if err != nil {
		t.Fatal(err)
	}

	queue, err := backgroundjobs.NewInsertClient(db, log)
	if err != nil {
		t.Fatal(err)
	}

	worker, err := backgroundjobs.NewWorkerClient(db, repos, replication.NewPusher(db, repos, nil, log), nil, nil,
		config.GarbageCollection{
			Interval:    time.Hour,
			GracePeriod: time.Hour,
		}, config.Trash{
			Retention: time.Hour,
		}, config.Audit{}, log)
	if err != nil {
		t.Fatal(err)
	}

	if err := worker.Start(ctx); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()

		_ = worker.Stop(stop)
	})

	srv := httptest.NewServer(app.NewHandler(db, blobs, tokens, queue, app.Options{}, log))
	t.Cleanup(srv.Close)

	if err := repos.CreateRepository(
		ctx,
		repository.Repository{
			Name:   "libs",
			Class:  repository.ClassLocal,
			Format: "generic",
		},
	); err != nil {
		t.Fatal(err)
	}

	item, err := repos.Deploy(ctx, repository.DeployInput{
		Repository: "libs",
		Path:       "lost.bin",
		Body:       strings.NewReader("lost content"),
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := blobs.Delete(ctx, objectstore.Key(item.Checksums.SHA256)); err != nil {
		t.Fatal(err)
	}

	cfg := filepath.Join(t.TempDir(), "config.yaml")
	if _, err := relic(
		t,
		cfg,
		"password\n",
		"auth",
		"login",
		srv.URL,
		"--user",
		"admin",
		"--password-stdin",
	); err != nil {
		t.Fatal(err)
	}

	out, err := relic(t, cfg, "", "storage", "check", "--wait", "-o", "json")
	if !errors.Is(err, exitCode1) {
		t.Fatalf("storage check with missing content = %v, want exit status 1; output %s", err, out)
	}

	var report storageReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("report %q: %v", out, err)
	}

	if report.Missing != 1 || len(report.Problems) != 1 || report.Problems[0].SHA256 != item.Checksums.SHA256 {
		t.Errorf("report = %+v, want the one missing blob", report)
	}
}
