package main

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/packages"
)

func TestSQLiteBridgeUpToDate(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	want, err := generate(root, false)
	if err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(filepath.Join(root, outFile))
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(got, want) {
		t.Error(outFile + " is stale: run go run ./tools/sqlitebridge")
	}
}

func TestBuildsWithoutTheDatabasePackage(t *testing.T) {
	t.Parallel()

	pkgs, err := packages.Load(&packages.Config{
		Mode: packages.NeedName | packages.NeedImports | packages.NeedDeps,
		Dir:  ".",
	}, ".")
	if err != nil {
		t.Fatal(err)
	}

	packages.Visit(pkgs, func(p *packages.Package) bool {
		if p.PkgPath == databasePkg {
			t.Errorf(
				"sqlitebridge imports %s, so it cannot build while sqlite_queries.go lacks a new query",
				databasePkg,
			)
		}

		return true
	}, nil)
}

func TestSQLiteQueriesPrepare(t *testing.T) {
	t.Parallel()

	if err := prepareAll(filepath.Join("..", "..")); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareChecksEveryStatement(t *testing.T) {
	t.Parallel()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = db.Close()
	})

	tests := []struct {
		name    string
		query   string
		wantErr bool
	}{
		{
			name:  "two valid statements",
			query: "SELECT 1;\nSELECT 2;",
		},
		{
			name:    "a broken second statement",
			query:   "SELECT 1;\nSELECT * FROM no_such_table;",
			wantErr: true,
		},
		{
			name:  "semicolons inside literals and comments",
			query: "-- one; two\nSELECT 'a;b', \"c;d\" FROM (SELECT 1 AS \"c;d\") /* ; */;",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if err := prepare(t.Context(), db, tt.query); (err != nil) != tt.wantErr {
				t.Errorf("prepare(%q) = %v, want error %v", tt.query, err, tt.wantErr)
			}
		})
	}
}
