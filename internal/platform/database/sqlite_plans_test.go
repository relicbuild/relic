package database

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSQLiteEnsureFoldersLooksUpEachFolderByKey(t *testing.T) {
	t.Parallel()

	db := newSQLiteTestDB(t)

	_, lookup, found := strings.Cut(sqliteEnsureFolders, ";\n")
	if !found {
		t.Fatalf("EnsureFolders has no second statement: %s", sqliteEnsureFolders)
	}

	plan := queryPlan(
		t,
		db,
		lookup,
		sql.Named("repo_key", "r"),
		sql.Named("paths", `["a"]`),
		sql.Named("names", `["b"]`),
	)

	if !slices.ContainsFunc(plan, func(step string) bool {
		return strings.Contains(step, "(repo_key=? AND path=? AND name=?)")
	}) {
		t.Errorf("EnsureFolders reads every item of the repository: %q", plan)
	}
}

func TestSQLiteSubtreeQueriesReadOnlyTheSubtree(t *testing.T) {
	t.Parallel()

	db := newSQLiteTestDB(t)
	subtree := []any{
		sql.Named("repo_key", "r"),
		sql.Named("path", "org"),
		sql.Named("name", "app"),
		sql.Named("full_path", "org/app"),
		sql.Named("prefix", "org/app/%"),
	}

	tests := []struct {
		name  string
		query string
		args  []any
	}{
		{
			name:  "DeleteSubtree",
			query: sqliteDeleteSubtree,
			args:  subtree,
		},
		{
			name:  "ListSubtree",
			query: sqliteListSubtree,
			args:  subtree,
		},
		{
			name:  "ListSubtreePage",
			query: sqliteListSubtreePage,
			args: append(
				slices.Clone(subtree),
				sql.Named("after_path", ""),
				sql.Named("after_name", ""),
				sql.Named("max", 100),
			),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			plan := queryPlan(t, db, tt.query, tt.args...)

			if !slices.ContainsFunc(plan, func(step string) bool {
				return strings.Contains(step, "path<?)")
			}) {
				t.Errorf("%s does not search the subtree's range of paths: %q", tt.name, plan)
			}

			if slices.ContainsFunc(plan, func(step string) bool {
				readsItems := strings.HasPrefix(step, "SCAN items") || strings.HasPrefix(step, "SEARCH items")

				return readsItems && !strings.Contains(step, "path=?") && !strings.Contains(step, "path<?)")
			}) {
				t.Errorf("%s reads every item of the repository: %q", tt.name, plan)
			}
		})
	}
}

func queryPlan(t *testing.T, db *DB, query string, args ...any) []string {
	t.Helper()

	rows, err := db.Query(t.Context(), "EXPLAIN QUERY PLAN "+query, args...)
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

	return plan
}

func TestSQLiteListItemsAtTakesTimeInProportionToItsPaths(t *testing.T) {
	t.Parallel()

	db := newSQLiteTestDB(t)
	stored := seedSQLiteItem(t, db)

	const count = 20_000

	paths := make([]string, 0, count)
	names := make([]string, 0, count)

	for i := range count - 1 {
		paths = append(paths, "org/app")
		names = append(names, fmt.Sprintf("missing-%d.jar", i))
	}

	paths = append(paths, stored.Path)
	names = append(names, stored.Name)

	start := time.Now()

	items, err := db.ListItemsAt(t.Context(), ListItemsAtParams{
		RepoKey: stored.RepoKey,
		Paths:   paths,
		Names:   names,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(items) != 1 || items[0].Name != stored.Name {
		t.Errorf("ListItemsAt = %d items, want %s", len(items), stored.Name)
	}

	if elapsed := time.Since(start); elapsed > listItemsAtDeadline {
		t.Errorf("ListItemsAt of %d paths took %s", count, elapsed)
	}
}

// A JSON array joined to another by key is read once per row of the other:
// zipping two arrays of n elements reads n² rows.
func TestSQLiteQueriesNeverJoinJSONArraysDirectly(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob(filepath.Join("..", "..", "..", "queries", "sqlite", "*.sql"))
	if err != nil {
		t.Fatal(err)
	}

	joined := regexp.MustCompile(`JOIN json_each\([^)]*\) AS \w+ ON`)

	for _, file := range files {
		text, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}

		for _, join := range joined.FindAllString(string(text), -1) {
			t.Errorf("%s: %s; materialize each array in a CTE and join those", filepath.Base(file), join)
		}
	}
}
