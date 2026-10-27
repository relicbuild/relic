package database

import (
	"slices"
	"strings"
	"testing"
)

func insertSQLiteFolders(t testing.TB, db *DB, repoKey string, paths ...string) {
	t.Helper()

	engine := db.engine.(*sqliteEngine)

	if _, err := engine.writer.ExecContext(
		t.Context(),
		`INSERT INTO repositories (key, class, format, project, config) VALUES (?, 'local', 'generic', 'default', '{}')`,
		repoKey,
	); err != nil {
		t.Fatal(err)
	}

	for _, full := range paths {
		folder, name := "", full

		if i := strings.LastIndex(full, "/"); i >= 0 {
			folder, name = full[:i], full[i+1:]
		}

		if _, err := engine.writer.ExecContext(t.Context(), `
INSERT INTO items (id, repo_key, path, name, type, created, created_by, modified, modified_by, updated)
VALUES (lower(hex(randomblob(16))), ?, ?, ?, 'folder', '2026-01-01', 'admin', '2026-01-01', 'admin', '2026-01-01')`,
			repoKey,
			folder,
			name,
		); err != nil {
			t.Fatal(err)
		}
	}
}

func subtreeLikePrefix(full string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(full) + "/%"
}

func joinedPaths(items []Item) []string {
	paths := make([]string, 0, len(items))

	for _, item := range items {
		if item.Path == "" {
			paths = append(paths, item.Name)

			continue
		}

		paths = append(paths, item.Path+"/"+item.Name)
	}

	return paths
}

func TestSQLiteSubtreeQueriesMatchOnlyTheSubtree(t *testing.T) {
	t.Parallel()

	tree := []string{
		"a",
		"a/b",
		"a/b/c",
		"a/b/c/d",
		"a/b-c",
		"a/b.c",
		"a/b0",
		"a/bc",
		"a/bc/d",
		"a/B",
		"a/b%",
		"a/b%/c",
		"a/b_",
		"a/b_/c",
		`a/b\`,
		`a/b\/c`,
		`a/b\x`,
		"a/b/ü",
		"a/b/ü/x",
		"a/bü",
	}

	tests := []struct {
		name string
		root string
		want []string
	}{
		{
			name: "a plain folder",
			root: "a/b",
			want: []string{
				"a/b",
				"a/b/c",
				"a/b/c/d",
				"a/b/ü",
				"a/b/ü/x",
			},
		},
		{
			name: "a folder named with a percent sign",
			root: "a/b%",
			want: []string{
				"a/b%",
				"a/b%/c",
			},
		},
		{
			name: "a folder named with an underscore",
			root: "a/b_",
			want: []string{
				"a/b_",
				"a/b_/c",
			},
		},
		{
			name: "a folder named with a backslash",
			root: `a/b\`,
			want: []string{
				`a/b\`,
				`a/b\/c`,
			},
		},
		{
			name: "a folder at the root",
			root: "a",
			want: tree,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			db := newSQLiteTestDB(t)
			insertSQLiteFolders(t, db, "libs", tree...)

			want := slices.Sorted(slices.Values(tt.want))
			folder, name := "", tt.root

			if i := strings.LastIndex(tt.root, "/"); i >= 0 {
				folder, name = tt.root[:i], tt.root[i+1:]
			}

			listed, err := db.ListSubtree(t.Context(), ListSubtreeParams{
				RepoKey:  "libs",
				Path:     folder,
				Name:     name,
				FullPath: tt.root,
				Prefix:   subtreeLikePrefix(tt.root),
			})
			if err != nil {
				t.Fatal(err)
			}

			if got := slices.Sorted(slices.Values(joinedPaths(listed))); !slices.Equal(got, want) {
				t.Errorf("ListSubtree(%q) = %q, want %q", tt.root, got, want)
			}

			var (
				paged     []Item
				afterPath string
				afterName string
				pages     = 0
			)

			for pages < 100 {
				pages++

				page, err := db.ListSubtreePage(t.Context(), ListSubtreePageParams{
					RepoKey:   "libs",
					Path:      folder,
					Name:      name,
					FullPath:  tt.root,
					Prefix:    subtreeLikePrefix(tt.root),
					AfterPath: afterPath,
					AfterName: afterName,
					Max:       2,
				})
				if err != nil {
					t.Fatal(err)
				}

				if len(page) == 0 {
					break
				}

				paged = append(paged, page...)
				afterPath, afterName = page[len(page)-1].Path, page[len(page)-1].Name
			}

			if got := joinedPaths(paged); !slices.Equal(got, joinedPaths(listed)) {
				t.Errorf(
					"ListSubtreePage(%q) pages = %q, want ListSubtree's order %q",
					tt.root,
					got,
					joinedPaths(listed),
				)
			}

			deleted, err := db.DeleteSubtree(t.Context(), DeleteSubtreeParams{
				RepoKey:  "libs",
				Path:     folder,
				Name:     name,
				FullPath: tt.root,
				Prefix:   subtreeLikePrefix(tt.root),
			})
			if err != nil {
				t.Fatal(err)
			}

			if len(deleted) != len(want) {
				t.Errorf("DeleteSubtree(%q) deleted %d items, want %d", tt.root, len(deleted), len(want))
			}

			left, err := db.ListRepositoryItems(t.Context(), "libs")
			if err != nil {
				t.Fatal(err)
			}

			for _, path := range joinedPaths(left) {
				if slices.Contains(want, path) {
					t.Errorf("DeleteSubtree(%q) left %q", tt.root, path)
				}
			}

			if len(left)+len(want) != len(tree) {
				t.Errorf(
					"DeleteSubtree(%q) left %d of %d items, want %d",
					tt.root,
					len(left),
					len(tree),
					len(tree)-len(want),
				)
			}
		})
	}
}
