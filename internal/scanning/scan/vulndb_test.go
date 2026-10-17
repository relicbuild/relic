package scan

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"relic/internal/platform/objectstore/objectstoretest"
	"relic/internal/platform/scripttest"
)

func fakeGrype(t *testing.T) (Grype, string) {
	t.Helper()

	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	script := filepath.Join(dir, "grype")

	scripttest.Write(t, script, `#!/bin/sh
echo "$* url=$GRYPE_DB_UPDATE_URL" >>"`+log+`"
if [ "$1 $2" = "db import" ]; then
  mkdir -p "$GRYPE_DB_CACHE_DIR/6" && cp "$3" "$GRYPE_DB_CACHE_DIR/6/vulnerability.db" && echo '{}' >"$GRYPE_DB_CACHE_DIR/6/import.json"
fi
`)

	return Grype{
		Exec: Exec{
			Path: script,
		},
		DatabaseDir: filepath.Join(dir, "db"),
	}, log
}

func calls(t *testing.T, log string) string {
	t.Helper()

	b, err := os.ReadFile(log)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}

	return string(b)
}

// Serial: a test forking while this one writes its script would keep the
// script's file open for writing (exec fails with "text file busy").
//
//nolint:paralleltest // see above
func TestGrypeUpdateDB(t *testing.T) {
	g, log := fakeGrype(t)
	g.UpdateURL = "https://mirror.example/grype/databases/v6/latest.json"

	if err := g.UpdateDB(t.Context()); err != nil {
		t.Fatal(err)
	}

	if got := calls(t, log); got != "db update url=https://mirror.example/grype/databases/v6/latest.json\n" {
		t.Errorf("calls = %q", got)
	}

	offline, offlineLog := fakeGrype(t)
	offline.Offline = true

	if err := offline.UpdateDB(t.Context()); err != nil || calls(t, offlineLog) != "" {
		t.Errorf("offline update ran grype: %q %v", calls(t, offlineLog), err)
	}
}

type undeletableStore struct {
	*objectstoretest.Memory
}

func (undeletableStore) Delete(context.Context, string) error {
	return errors.New("delete refused")
}

//nolint:paralleltest // see TestGrypeUpdateDB
func TestDatabaseImports(t *testing.T) {
	blobs := objectstoretest.NewMemory()
	imports := NewDatabaseImports(blobs, slog.New(slog.DiscardHandler))
	g, log := fakeGrype(t)
	g.Offline = true

	key, err := imports.Stage(t.Context(), strings.NewReader("the database"), int64(len("the database")))
	if err != nil || !strings.HasPrefix(key, DatabaseImportPrefix) {
		t.Fatalf("stage = %q %v", key, err)
	}

	if err := imports.Install(t.Context(), g, key); err != nil {
		t.Fatal(err)
	}

	if !g.HasDB() || !strings.HasPrefix(calls(t, log), "db import ") {
		t.Errorf("installed %v, calls %q", g.HasDB(), calls(t, log))
	}

	if b, _ := os.ReadFile(filepath.Join(g.DatabaseDir, "6", "vulnerability.db")); string(b) != "the database" {
		t.Errorf("database = %q", b)
	}

	if _, err := blobs.Head(t.Context(), key); err == nil {
		t.Error("the staged archive is still in object storage")
	}

	var logged bytes.Buffer

	undeletable := NewDatabaseImports(undeletableStore{
		Memory: objectstoretest.NewMemory(),
	}, slog.New(slog.NewTextHandler(&logged, nil)))

	kept, err := undeletable.Stage(t.Context(), strings.NewReader("the database"), int64(len("the database")))
	if err != nil {
		t.Fatal(err)
	}

	if err := undeletable.Install(t.Context(), g, kept); err != nil {
		t.Errorf("install whose staged archive cannot be removed = %v, want the installed database reported", err)
	}

	if !strings.Contains(logged.String(), kept) {
		t.Errorf("the staged archive left in object storage is not logged: %q", logged.String())
	}

	for _, bad := range []string{
		"sha256/aa/bb/digest",
		DatabaseImportPrefix + "../../sha256/aa",
	} {
		if err := imports.Install(t.Context(), g, bad); !errors.Is(err, ErrInvalidDatabaseImport) {
			t.Errorf("install %q: %v", bad, err)
		}

		if err := imports.Discard(t.Context(), bad); !errors.Is(err, ErrInvalidDatabaseImport) {
			t.Errorf("discard %q: %v", bad, err)
		}
	}

	unqueued, err := imports.Stage(t.Context(), strings.NewReader("not queued"), int64(len("not queued")))
	if err != nil {
		t.Fatal(err)
	}

	if err := imports.Discard(t.Context(), unqueued); err != nil {
		t.Fatal(err)
	}

	if _, err := blobs.Head(t.Context(), unqueued); err == nil {
		t.Error("a discarded archive is still in object storage")
	}
}
