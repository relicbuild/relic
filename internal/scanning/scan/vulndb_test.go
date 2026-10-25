package scan

import (
	"bytes"
	"context"
	"errors"
	"io"
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

func TestDatabaseArchive(t *testing.T) {
	t.Parallel()

	tarHeader := make([]byte, 512)
	copy(tarHeader, "vulnerability.db")
	copy(tarHeader[257:], "ustar")

	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{
			name: "zstd",
			body: "\x28\xb5\x2f\xfd" + "the database",
		},
		{
			name: "gzip",
			body: "\x1f\x8b\x08" + "the database",
		},
		{
			name: "xz",
			body: "\xfd7zXZ\x00" + "the database",
		},
		{
			name: "tar",
			body: string(tarHeader) + "the database",
		},
		{
			name:    "text",
			body:    "garbage\n",
			wantErr: true,
		},
		{
			name:    "empty",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r, err := DatabaseArchive(strings.NewReader(tt.body))
			if tt.wantErr {
				if !errors.Is(err, ErrNotDatabaseArchive) {
					t.Errorf("DatabaseArchive = %v, want ErrNotDatabaseArchive", err)
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			if b, err := io.ReadAll(r); err != nil || string(b) != tt.body {
				t.Errorf("the archive read back = %q, %v; want it whole", b, err)
			}
		})
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

// invalidDBGrype is a Grype whose installed database fails its checksum
// until it is deleted and downloaded again; scans fail while it is invalid.
func invalidDBGrype(t *testing.T) (Grype, string) {
	t.Helper()

	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	script := filepath.Join(dir, "grype")
	db := filepath.Join(dir, "db", "6")

	if err := os.MkdirAll(db, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(db, "import.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	scripttest.Write(t, script, `#!/bin/sh
echo "$*" >>"`+log+`"
db="$GRYPE_DB_CACHE_DIR/6"
case "$1 $2" in
"db status")
  if [ -e "$db/fresh" ]; then echo '{"built":"2026-10-18T06:27:17Z","valid":true}'
  else echo '{"built":"2026-10-18T06:27:17Z","valid":false,"error":"bad db checksum"}'; fi ;;
"db delete") rm -rf "$db" ;;
"db update")
  if [ -e "$db/import.json" ]; then echo "No vulnerability database update available"
  else mkdir -p "$db" && echo '{}' >"$db/import.json" && touch "$db/fresh"; fi ;;
*) if [ -e "$db/fresh" ]; then echo '{"matches":[]}'; else echo "ERROR bad db checksum" >&2; exit 1; fi ;;
esac
`)

	return Grype{
		Exec: Exec{
			Path: script,
		},
		DatabaseDir: filepath.Join(dir, "db"),
	}, log
}

//nolint:paralleltest // see TestGrypeUpdateDB
func TestGrypeReplacesAnInvalidDB(t *testing.T) {
	g, log := invalidDBGrype(t)

	if _, err := g.DBBuilt(t.Context()); !errors.Is(err, ErrInvalidDB) {
		t.Fatalf("DBBuilt of an invalid database = %v, want ErrInvalidDB", err)
	}

	if _, err := g.Match(t.Context(), "sbom.json"); !errors.Is(err, ErrInvalidDB) {
		t.Fatalf("Match with an invalid database = %v, want ErrInvalidDB", err)
	}

	replaced, err := g.ReplaceInvalidDB(t.Context())
	if err != nil || !replaced {
		t.Fatalf("ReplaceInvalidDB = %v, %v; calls %q", replaced, err, calls(t, log))
	}

	if _, err := g.DBBuilt(t.Context()); err != nil {
		t.Fatalf("DBBuilt after the replacement: %v", err)
	}

	if _, err := g.Match(t.Context(), "sbom.json"); err != nil {
		t.Fatalf("Match after the replacement: %v", err)
	}

	if err := os.Truncate(log, 0); err != nil {
		t.Fatal(err)
	}

	if replaced, err := g.ReplaceInvalidDB(t.Context()); err != nil || replaced {
		t.Errorf("ReplaceInvalidDB of a valid database = %v, %v", replaced, err)
	}

	if got := calls(t, log); got != "db status -o json\n" {
		t.Errorf("a valid database ran %q, want only its status", got)
	}
}

//nolint:paralleltest // see TestGrypeUpdateDB
func TestGrypeKeepsAnInvalidDBOffline(t *testing.T) {
	g, log := invalidDBGrype(t)
	g.Offline = true

	replaced, err := g.ReplaceInvalidDB(t.Context())
	if !errors.Is(err, ErrInvalidDB) || replaced {
		t.Errorf("offline ReplaceInvalidDB = %v, %v; want ErrInvalidDB", replaced, err)
	}

	if got := calls(t, log); strings.Contains(got, "db delete") || strings.Contains(got, "db update") {
		t.Errorf("offline, the invalid database was replaced: %q", got)
	}
}
