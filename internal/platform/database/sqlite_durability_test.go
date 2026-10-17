package database_test

import (
	"os"
	"path/filepath"
	"testing"

	"relic/internal/platform/database"
)

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
