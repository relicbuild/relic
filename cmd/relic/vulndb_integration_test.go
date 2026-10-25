//go:build integration

package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"relic/api/adapters/artifactory/access"
	"relic/internal/access/auth"
	"relic/internal/app"
	"relic/internal/app/backgroundjobs"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
	"relic/internal/scanning/scan"
)

type importQueue struct {
	imports *scan.DatabaseImports
	grype   scan.Grype
}

func (q importQueue) Insert(
	ctx context.Context,
	args river.JobArgs,
	_ *river.InsertOpts,
) (*rivertype.JobInsertResult, error) {
	a, ok := args.(backgroundjobs.ScanDBImportArgs)
	if !ok {
		return nil, errors.New("unexpected job " + args.Kind())
	}

	return &rivertype.JobInsertResult{}, q.imports.Install(ctx, q.grype, a.Key)
}

func TestVulnerabilityDatabaseCommands(t *testing.T) {
	t.Parallel()

	db := databasetest.NewDB(t)
	log := slog.New(slog.DiscardHandler)

	if err := app.BootstrapAdmin(t.Context(), db, "admin", "password", log); err != nil {
		t.Fatal(err)
	}

	tokens, err := auth.NewTokens(t.Context(), db, nil, access.Format{})
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	grypePath := filepath.Join(dir, "grype")

	//nolint:gosec // a test script
	if err := os.WriteFile(grypePath, []byte(`#!/bin/sh
[ "$1 $2" = "db import" ] && mkdir -p "$GRYPE_DB_CACHE_DIR/6" && cp "$3" "$GRYPE_DB_CACHE_DIR/6/vulnerability.db" && echo '{}' >"$GRYPE_DB_CACHE_DIR/6/import.json"
`), 0o700); err != nil {
		t.Fatal(err)
	}

	blobs := objectstoretest.NewMemory()
	grype := scan.Grype{
		Exec: scan.Exec{
			Path: grypePath,
		},
		DatabaseDir: filepath.Join(dir, "db"),
		Offline:     true,
	}
	srv := httptest.NewServer(app.NewHandler(db, blobs, tokens, importQueue{
		imports: scan.NewDatabaseImports(blobs, slog.New(slog.DiscardHandler)),
		grype:   grype,
	}, app.Options{}, log))
	t.Cleanup(srv.Close)

	config := filepath.Join(dir, "config.yaml")
	if _, err := relic(
		t,
		config,
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

	const zstdArchive = "\x28\xb5\x2f\xfd" + "database archive"

	archive := filepath.Join(dir, "grype-db.tar.zst")
	if err := os.WriteFile(archive, []byte(zstdArchive), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := relic(t, config, "", "security", "database", "import", archive); err == nil ||
		!strings.Contains(err.Error(), "Relic Scan is off") {
		t.Errorf("import without a worker that scans = %v, want it refused", err)
	}

	worker := `INSERT INTO river_queue (name, updated_at) VALUES ('scan', now())`
	if db.SQLite() {
		worker = `INSERT INTO river_queue (name, updated_at) VALUES ('scan', datetime('now', 'subsec'))`
	}

	rows, err := db.Query(t.Context(), worker)
	if err != nil {
		t.Fatal(err)
	}

	rows.Close()

	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	garbage := filepath.Join(dir, "bad.tar.zst")
	if err := os.WriteFile(garbage, []byte("garbage\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := relic(t, config, "", "security", "database", "import", garbage); err == nil ||
		!strings.Contains(err.Error(), "not a vulnerability database archive") {
		t.Errorf("import of a file that is no archive = %v, want it refused", err)
	}

	out, err := relic(t, config, "", "security", "database", "import", archive)
	if err != nil || !strings.Contains(out, "uploaded; the worker installs it") {
		t.Fatalf("import = %q %v", out, err)
	}

	if b, _ := os.ReadFile(filepath.Join(grype.DatabaseDir, "6", "vulnerability.db")); string(b) != zstdArchive {
		t.Errorf("installed database = %q", b)
	}

	if out, err := relic(
		t,
		config,
		"",
		"security",
		"database",
		"status",
	); err != nil ||
		out != "no scan has used a vulnerability database yet\n" {
		t.Errorf("status = %q %v", out, err)
	}

	if _, err := relic(t, config, `apiVersion: relic.build/v1
kind: User
metadata: {name: vuln-dev}
spec: {email: dev@example.com, password: Dev-pass-12345}
`, "apply", "-f", "-"); err != nil {
		t.Fatal(err)
	}

	dev := filepath.Join(dir, "dev.yaml")
	if _, err := relic(
		t,
		dev,
		"Dev-pass-12345\n",
		"auth", "login",
		srv.URL,
		"--user",
		"vuln-dev",
		"--password-stdin",
	); err != nil {
		t.Fatal(err)
	}

	if _, err := relic(t, dev, "", "security", "database", "import", archive); err == nil ||
		!strings.Contains(err.Error(), "403") {
		t.Errorf("a non-administrator imported: %v", err)
	}
}
