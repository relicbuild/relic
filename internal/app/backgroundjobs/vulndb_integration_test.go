//go:build integration

package backgroundjobs_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"relic/internal/app/backgroundjobs"
	"relic/internal/app/config"
	"relic/internal/artifacts/repository"
	"relic/internal/delivery/replication"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
	"relic/internal/scanning/scan"
)

func TestVulnerabilityDatabaseImport(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := databasetest.NewDB(t)
	log := slog.New(slog.DiscardHandler)
	blobs := objectstoretest.NewMemory()
	repos := repository.NewService(db, blobs, log)
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	report, _ := filepath.Abs("../../scanning/scan/testdata/grype.json")

	grypePath := filepath.Join(dir, "grype")

	//nolint:gosec // a test script
	if err := os.WriteFile(grypePath, []byte(`#!/bin/sh
echo "$*" >>"`+calls+`"
db="$GRYPE_DB_CACHE_DIR/6"
case "$1 $2" in
"db import") mkdir -p "$db" && cp "$3" "$db/vulnerability.db" && echo '{}' >"$db/import.json" ;;
"db status")
  if [ -e "$db/import.json" ] && [ ! -e "`+dir+`/failed" ]; then touch "`+dir+`/failed"; exit 1; fi
  echo '{"built":"2026-09-29T00:00:00Z"}' ;;
"db update") exit 1 ;;
*) cat "$db/vulnerability.db" ;;
esac
`), 0o700); err != nil {
		t.Fatal(err)
	}

	grype := scan.Grype{
		Exec: scan.Exec{
			Path: grypePath,
		},
		DatabaseDir: filepath.Join(dir, "db"),
		Offline:     true,
	}
	imports := scan.NewDatabaseImports(blobs, slog.New(slog.DiscardHandler))
	scans := scan.NewService(db, blobs, nil, grype, scan.Limits{}, log)

	worker, err := backgroundjobs.NewWorkerClient(
		db,
		repos,
		replication.NewPusher(db, repos, nil, log),
		&backgroundjobs.Scanning{
			Service: scans,
			Grype:   grype,
			Imports: imports,
		},
		nil,
		config.GarbageCollection{
			Interval:    time.Hour,
			GracePeriod: time.Hour,
		},
		config.Trash{
			Retention: time.Hour,
		},
		config.Audit{},
		log,
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := worker.Start(ctx); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()

		_ = worker.Stop(stopCtx)
	})

	archive, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}

	key, err := imports.Stage(ctx, strings.NewReader(string(archive)), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}

	inserter, err := backgroundjobs.NewInsertClient(db, log)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := inserter.Insert(ctx, backgroundjobs.ScanDBImportArgs{
		Key: key,
	}, nil); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(30 * time.Second)
	for !grype.HasDB() {
		if time.Now().After(deadline) {
			b, _ := os.ReadFile(calls)
			t.Fatalf("the database was not imported in 30s; grype calls:\n%s", b)
		}

		time.Sleep(100 * time.Millisecond)
	}

	deadline = time.Now().Add(30 * time.Second)

	for {
		if _, err := blobs.Head(ctx, key); err != nil {
			break
		}

		if time.Now().After(deadline) {
			t.Fatal("the staged archive is still in object storage")
		}

		time.Sleep(100 * time.Millisecond)
	}

	id, err := scans.QueueGraph(ctx, "admin", scan.GraphRequest{
		PURLs: []string{
			"pkg:npm/lodash@4.17.15",
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := scans.RunGraph(ctx, id, true); err != nil {
		t.Fatal(err)
	}

	g, err := scan.NewResults(db).GraphScan(ctx, id)
	if err != nil || g.Status != scan.StatusDone || !strings.Contains(string(g.Result), "GHSA-35jh-r3h4-6jhm") {
		t.Errorf("scan = %s %s %v", g.Status, g.Result, err)
	}

	if b, _ := os.ReadFile(calls); strings.Contains(string(b), "db update") {
		t.Errorf("grype calls:\n%s", b)
	}

	deadline = time.Now().Add(30 * time.Second)

	for {
		done, err := db.Jobs(ctx, "scan_db_import", "completed", 10)
		if err != nil {
			t.Fatal(err)
		}

		if len(done) == 1 {
			break
		}

		if time.Now().After(deadline) {
			b, _ := os.ReadFile(calls)
			t.Fatalf("the import job did not complete after its rematch failed once; grype calls:\n%s", b)
		}

		time.Sleep(100 * time.Millisecond)
	}

	b, _ := os.ReadFile(calls)
	if _, err := os.Stat(filepath.Join(dir, "failed")); err != nil || strings.Count(string(b), "db status") < 2 {
		t.Errorf("the import completed without its failed db status and a retry (%v); grype calls:\n%s", err, b)
	}
}

func TestVulnerabilityDatabaseImportDiscardsTheArchiveAfterItsLastAttempt(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := databasetest.NewDB(t)
	log := slog.New(slog.DiscardHandler)
	blobs := objectstoretest.NewMemory()
	repos := repository.NewService(db, blobs, log)
	dir := t.TempDir()
	grypePath := filepath.Join(dir, "grype")

	//nolint:gosec // a test script
	if err := os.WriteFile(grypePath, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	grype := scan.Grype{
		Exec: scan.Exec{
			Path: grypePath,
		},
		DatabaseDir: filepath.Join(dir, "db"),
		Offline:     true,
	}
	imports := scan.NewDatabaseImports(blobs, log)

	worker, err := backgroundjobs.NewWorkerClient(
		db,
		repos,
		replication.NewPusher(db, repos, nil, log),
		&backgroundjobs.Scanning{
			Service: scan.NewService(db, blobs, nil, grype, scan.Limits{}, log),
			Grype:   grype,
			Imports: imports,
		},
		nil,
		config.GarbageCollection{
			Interval:    time.Hour,
			GracePeriod: time.Hour,
		},
		config.Trash{
			Retention: time.Hour,
		},
		config.Audit{},
		log,
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := worker.Start(ctx); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()

		_ = worker.Stop(stopCtx)
	})

	key, err := imports.Stage(ctx, strings.NewReader("not a database"), int64(len("not a database")))
	if err != nil {
		t.Fatal(err)
	}

	inserter, err := backgroundjobs.NewInsertClient(db, log)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := inserter.Insert(ctx, backgroundjobs.ScanDBImportArgs{
		Key: key,
	}, &river.InsertOpts{
		MaxAttempts: 1,
	}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(30 * time.Second)

	for {
		if _, err := blobs.Head(ctx, key); err != nil {
			break
		}

		if time.Now().After(deadline) {
			t.Fatal("the archive of an import that failed its last attempt is still in object storage")
		}

		time.Sleep(100 * time.Millisecond)
	}
}

func TestVulnerabilityDatabaseUpdateReplacesAnInvalidDatabase(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := databasetest.NewDB(t)
	log := slog.New(slog.DiscardHandler)
	blobs := objectstoretest.NewMemory()
	repos := repository.NewService(db, blobs, log)
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	installed := filepath.Join(dir, "db", "6")

	if err := os.MkdirAll(installed, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(installed, "import.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	grypePath := filepath.Join(dir, "grype")

	//nolint:gosec // a test script
	if err := os.WriteFile(grypePath, []byte(`#!/bin/sh
echo "$*" >>"`+calls+`"
db="$GRYPE_DB_CACHE_DIR/6"
case "$1 $2" in
"db status")
  if [ -e "$db/fresh" ]; then echo '{"built":"2026-10-18T06:27:17Z","valid":true}'
  else echo '{"built":"2026-10-18T06:27:17Z","valid":false,"error":"bad db checksum"}'; fi ;;
"db delete") rm -rf "$db" ;;
"db update")
  if [ -e "$db/import.json" ]; then echo "No vulnerability database update available"
  else mkdir -p "$db" && echo '{}' >"$db/import.json" && touch "$db/fresh"; fi ;;
*) exit 1 ;;
esac
`), 0o700); err != nil {
		t.Fatal(err)
	}

	grype := scan.Grype{
		Exec: scan.Exec{
			Path: grypePath,
		},
		DatabaseDir: filepath.Join(dir, "db"),
	}

	worker, err := backgroundjobs.NewWorkerClient(
		db,
		repos,
		replication.NewPusher(db, repos, nil, log),
		&backgroundjobs.Scanning{
			Service: scan.NewService(db, blobs, nil, grype, scan.Limits{}, log),
			Grype:   grype,
			Imports: scan.NewDatabaseImports(blobs, log),
		},
		nil,
		config.GarbageCollection{
			Interval:    time.Hour,
			GracePeriod: time.Hour,
		},
		config.Trash{
			Retention: time.Hour,
		},
		config.Audit{},
		log,
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := worker.Start(ctx); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()

		_ = worker.Stop(stopCtx)
	})

	deadline := time.Now().Add(30 * time.Second)

	for {
		if _, err := os.Stat(filepath.Join(installed, "fresh")); err == nil {
			break
		}

		if time.Now().After(deadline) {
			b, _ := os.ReadFile(calls)
			t.Fatalf("the invalid database was not replaced in 30s; grype calls:\n%s", b)
		}

		time.Sleep(100 * time.Millisecond)
	}

	if _, err := grype.DBBuilt(ctx); err != nil {
		t.Errorf("the replaced database: %v", err)
	}
}
