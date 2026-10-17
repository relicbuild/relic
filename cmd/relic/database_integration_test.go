//go:build integration

package main

import (
	"path/filepath"
	"strings"
	"testing"

	"relic/internal/platform/database"
	"relic/internal/platform/database/databasetest"
)

func TestDatabaseCopyAndBackup(t *testing.T) {
	t.Parallel()
	databasetest.RequirePostgreSQL(t)

	ctx := t.Context()
	src := databasetest.NewDB(t)

	if _, err := src.UpsertGroup(ctx, database.UpsertGroupParams{
		Name:        "devs",
		Description: "developers",
	}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	config := filepath.Join(dir, "config.yaml")
	lite := "sqlite://" + filepath.Join(dir, "relic.db")

	out, err := relic(
		t,
		config,
		"",
		"server",
		"database",
		"copy",
		"--from",
		src.Pool().Config().ConnString(),
		"--to",
		lite,
	)
	if err != nil || !strings.Contains(out, "copied ") {
		t.Fatalf("copy = %q, %v", out, err)
	}

	db, err := database.Open(ctx, lite)
	if err != nil {
		t.Fatal(err)
	}

	g, err := db.GetGroup(ctx, "devs")
	db.Close()

	if err != nil || g.Description != "developers" {
		t.Fatalf("the copied group = %+v, %v", g, err)
	}

	if _, err := relic(
		t,
		config,
		"",
		"server",
		"database",
		"copy",
		"--from",
		src.Pool().Config().ConnString(),
		"--to",
		lite,
	); err == nil ||
		!strings.Contains(err.Error(), "not empty") {
		t.Errorf("a copy into a database with data = %v", err)
	}

	backup := filepath.Join(dir, "backup.db")

	out, err = relic(t, config, "", "server", "database", "backup", "--database-url", lite, backup)
	if err != nil || !strings.Contains(out, "backed up") {
		t.Fatalf("backup = %q, %v", out, err)
	}

	snap, err := database.Open(ctx, "sqlite://"+backup)
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close()

	if _, err := snap.GetGroup(ctx, "devs"); err != nil {
		t.Errorf("the backup lacks the group: %v", err)
	}
}
