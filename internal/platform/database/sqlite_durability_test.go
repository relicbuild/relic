package database_test

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"relic/internal/platform/database"
)

func TestSQLiteLeavesNoOverwrittenTextInItsFiles(t *testing.T) {
	t.Parallel()

	const secret = "PLAINREMOTEPW"

	path := filepath.Join(t.TempDir(), "relic.db")

	db, err := database.Open(t.Context(), "sqlite://"+path)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(db.Close)

	for _, statement := range []string{
		"CREATE TABLE settings (name text PRIMARY KEY, config text NOT NULL)",
		"INSERT INTO settings VALUES ('remote', '{\"note\":\"" + strings.Repeat("x", 6000) +
			"\",\"password\":\"" + secret + "\"}')",
		"INSERT INTO settings VALUES ('other', '" + strings.Repeat("y", 3000) + "')",
		"DELETE FROM settings WHERE name = 'remote'",
		"INSERT INTO settings VALUES ('remote', '{\"password\":\"sealed\"}')",
	} {
		rows, err := db.Query(t.Context(), statement)
		if err != nil {
			t.Fatalf("%s: %v", statement, err)
		}

		rows.Close()
	}

	if err := db.TruncateLog(t.Context()); err != nil {
		t.Fatal(err)
	}

	for _, file := range []string{
		path,
		path + "-wal",
	} {
		content, err := os.ReadFile(file)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Fatal(err)
		}

		if bytes.Contains(content, []byte(secret)) {
			t.Errorf("%s still holds the overwritten text", filepath.Base(file))
		}
	}
}

func TestSQLiteCommitsAreDurable(t *testing.T) {
	t.Parallel()

	for _, dir := range []string{
		"data",
		"a?b",
		"100%",
		"a#b",
	} {
		t.Run(dir, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), dir, "relic.db")
			if err := os.Mkdir(filepath.Dir(path), 0o750); err != nil {
				t.Fatal(err)
			}

			db, err := database.Open(t.Context(), "sqlite://"+path)
			if err != nil {
				t.Fatal(err)
			}

			t.Cleanup(db.Close)

			if _, err := os.Stat(path); err != nil {
				t.Errorf("the database is not at %s: %v", path, err)
			}

			rows, err := db.Query(t.Context(), "PRAGMA synchronous")
			if err != nil {
				t.Fatal(err)
			}

			defer rows.Close()

			if !rows.Next() {
				t.Fatal("PRAGMA synchronous returned no row")
			}

			var level int
			if err := rows.Scan(&level); err != nil {
				t.Fatal(err)
			}

			if level != 2 {
				t.Errorf("synchronous = %d, want 2 (FULL)", level)
			}
		})
	}
}
