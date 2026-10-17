// Package migrations embeds Relic's goose SQL migrations, one tree per
// database engine.
package migrations

import (
	"embed"
	"io/fs"
)

// A schema change adds a file with the same version to both trees.
var (
	//go:embed postgresql/*.sql
	postgresqlFiles embed.FS
	//go:embed sqlite/*.sql
	sqliteFiles embed.FS

	// PostgreSQL holds the PostgreSQL migrations.
	PostgreSQL = sub(postgresqlFiles, "postgresql")
	// SQLite holds the SQLite migrations.
	SQLite = sub(sqliteFiles, "sqlite")
)

func sub(files embed.FS, dir string) fs.FS {
	tree, err := fs.Sub(files, dir)
	if err != nil {
		panic(err)
	}

	return tree
}
