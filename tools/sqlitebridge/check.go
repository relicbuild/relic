package main

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"relic/migrations"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"

	_ "relic/internal/platform/database"
)

func prepareAll(root string) error {
	lite, err := readQueries(filepath.Join(root, "queries", "sqlite"))
	if err != nil {
		return err
	}

	dir, err := os.MkdirTemp("", "sqlitebridge-")
	if err != nil {
		return err
	}

	defer func() {
		_ = os.RemoveAll(dir)
	}()

	db, err := sql.Open("sqlite", filepath.Join(dir, "relic.db"))
	if err != nil {
		return err
	}

	defer func() {
		_ = db.Close()
	}()

	ctx := context.Background()

	provider, err := goose.NewProvider(goose.DialectSQLite3, db, migrations.SQLite)
	if err != nil {
		return err
	}

	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	var problems []string

	for _, name := range slices.Sorted(maps.Keys(lite)) {
		if err := prepare(ctx, db, lite[name].SQL); err != nil {
			problems = append(problems, fmt.Sprintf("%s (%s): %v", name, lite[name].File, err))
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("%d queries do not prepare:\n  %s", len(problems), strings.Join(problems, "\n  "))
	}

	return nil
}

func prepare(ctx context.Context, db *sql.DB, query string) error {
	for _, statement := range sqlStatements(query) {
		if err := prepareStatement(ctx, db, statement); err != nil {
			return err
		}
	}

	return nil
}

func prepareStatement(ctx context.Context, db *sql.DB, statement string) error {
	stmt, err := db.PrepareContext(ctx, statement)
	if err != nil {
		return err
	}

	defer func() {
		_ = stmt.Close()
	}()

	return nil
}

func sqlStatements(query string) []string {
	var (
		statements []string
		start      int
		quote      byte
		hasCode    bool
	)

	for i := 0; i < len(query); i++ {
		c := query[i]

		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"' || c == '`':
			quote = c
			hasCode = true
		case c == '-' && strings.HasPrefix(query[i:], "--"):
			end := strings.IndexByte(query[i:], '\n')
			if end < 0 {
				i = len(query)
			} else {
				i += end
			}
		case c == '/' && strings.HasPrefix(query[i:], "/*"):
			end := strings.Index(query[i+2:], "*/")
			if end < 0 {
				i = len(query)
			} else {
				i += end + 3
			}
		case c == ';':
			if hasCode {
				statements = append(statements, query[start:i+1])
			}

			start, hasCode = i+1, false
		case c != ' ' && c != '\t' && c != '\n' && c != '\r':
			hasCode = true
		}
	}

	if hasCode {
		statements = append(statements, query[start:])
	}

	return statements
}
