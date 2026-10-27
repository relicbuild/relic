package database

import (
	"database/sql"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSQLiteEnsureFoldersLooksUpEachFolderByKey(t *testing.T) {
	t.Parallel()

	db, err := Open(t.Context(), "sqlite://"+filepath.Join(t.TempDir(), "relic.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(db.Close)

	if _, err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}

	_, lookup, found := strings.Cut(sqliteEnsureFolders, ";\n")
	if !found {
		t.Fatalf("EnsureFolders has no second statement: %s", sqliteEnsureFolders)
	}

	rows, err := db.Query(
		t.Context(),
		"EXPLAIN QUERY PLAN "+lookup,
		sql.Named("repo_key", "r"),
		sql.Named("paths", `["a"]`),
		sql.Named("names", `["b"]`),
	)
	if err != nil {
		t.Fatal(err)
	}

	defer rows.Close()

	var plan []string

	for rows.Next() {
		var (
			id, parent, unused int
			step               string
		)

		if err := rows.Scan(&id, &parent, &unused, &step); err != nil {
			t.Fatal(err)
		}

		plan = append(plan, step)
	}

	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	if !slices.ContainsFunc(plan, func(step string) bool {
		return strings.Contains(step, "(repo_key=? AND path=? AND name=?)")
	}) {
		t.Errorf("EnsureFolders reads every item of the repository: %q", plan)
	}
}
