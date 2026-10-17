//go:build integration

package database_test

import (
	"database/sql"
	"fmt"
	"io/fs"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"relic/internal/platform/database/databasetest"
	"relic/migrations"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

type column struct {
	Kind       string
	NotNull    bool
	HasDefault bool
}

type table struct {
	Columns    map[string]column
	Order      []string
	PrimaryKey []string
}

var postgresqlOnlyIndexes = []string{
	"scan_findings_cves",
}

func TestSchemasMatch(t *testing.T) {
	t.Parallel()
	databasetest.RequirePostgreSQL(t)

	pg := databasetest.NewDB(t)
	lite := migratedSQLite(t)

	pgTables, pgIndexes := postgresqlSchema(t, pg.Pool().Config().ConnString())
	liteTables, liteIndexes := sqliteSchema(t, lite)

	for _, name := range slices.Sorted(maps.Keys(pgTables)) {
		lt, ok := liteTables[name]
		if !ok {
			t.Errorf("table %s is missing in SQLite", name)

			continue
		}

		pt := pgTables[name]
		for _, col := range slices.Sorted(maps.Keys(pt.Columns)) {
			if got, want := lt.Columns[col], pt.Columns[col]; got != want {
				t.Errorf("%s.%s: SQLite %+v, PostgreSQL %+v", name, col, got, want)
			}
		}

		for col := range lt.Columns {
			if _, ok := pt.Columns[col]; !ok {
				t.Errorf("%s.%s is only in SQLite", name, col)
			}
		}

		if !slices.Equal(lt.Order, pt.Order) {
			t.Errorf("%s column order: SQLite %v, PostgreSQL %v", name, lt.Order, pt.Order)
		}

		if !slices.Equal(lt.PrimaryKey, pt.PrimaryKey) {
			t.Errorf("%s primary key: SQLite %v, PostgreSQL %v", name, lt.PrimaryKey, pt.PrimaryKey)
		}
	}

	for name := range liteTables {
		if _, ok := pgTables[name]; !ok {
			t.Errorf("table %s is only in SQLite", name)
		}
	}

	wantIndexes := slices.DeleteFunc(pgIndexes, func(name string) bool {
		return slices.Contains(postgresqlOnlyIndexes, name)
	})
	if !slices.Equal(liteIndexes, wantIndexes) {
		t.Errorf("indexes:\n SQLite     %v\n PostgreSQL %v", liteIndexes, wantIndexes)
	}

	liteVersions := versions(t, migrations.SQLite)

	pgVersions := slices.DeleteFunc(versions(t, migrations.PostgreSQL), func(v int64) bool {
		return v < liteVersions[0]
	})
	if !slices.Equal(liteVersions, pgVersions) {
		t.Errorf("versions from %d on: SQLite %v, PostgreSQL %v", liteVersions[0], liteVersions, pgVersions)
	}
}

func TestSQLiteIdentityProvidersDownRestoresTheRealmChecks(t *testing.T) {
	t.Parallel()

	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "relic.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = db.Close()
	})

	provider, err := goose.NewProvider(goose.DialectSQLite3, db, migrations.SQLite)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := provider.UpTo(t.Context(), 34); err != nil {
		t.Fatal(err)
	}

	if _, err := provider.DownTo(t.Context(), 33); err != nil {
		t.Fatal(err)
	}

	for _, statement := range []string{
		`INSERT INTO users (id, username, password_hash, realm) VALUES ('1', 'sam', 'x', 'sso')`,
		`INSERT INTO groups (name) VALUES ('team')`,
		`INSERT INTO users (id, username, password_hash) VALUES ('2', 'ida', 'x')`,
		`INSERT INTO group_members (group_name, username, source) VALUES ('team', 'ida', 'sso')`,
	} {
		_, err := db.ExecContext(t.Context(), statement)
		if strings.Contains(statement, "'sso'") && err == nil {
			t.Errorf("%s succeeded after the down migration", statement)
		}

		if !strings.Contains(statement, "'sso'") && err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
}

func migratedSQLite(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "relic.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = db.Close()
	})

	provider, err := goose.NewProvider(goose.DialectSQLite3, db, migrations.SQLite)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatalf("migrate SQLite: %v", err)
	}

	return db
}

func postgresqlSchema(t *testing.T, url string) (map[string]table, []string) {
	t.Helper()

	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		_ = db.Close()
	}()

	tables := map[string]table{}
	rows := query(t, db, `
SELECT table_name, column_name, data_type, is_nullable = 'NO', column_default IS NOT NULL
FROM information_schema.columns
WHERE table_schema = 'public' AND table_name NOT LIKE 'river_%' AND table_name <> 'goose_db_version'
ORDER BY table_name, ordinal_position`)

	for _, r := range rows {
		tbl := tableOf(tables, r[0])
		tbl.Columns[r[1]] = column{
			Kind:       postgresqlKind(r[2]),
			NotNull:    r[3] == "true",
			HasDefault: r[4] == "true",
		}
		tbl.Order = append(tbl.Order, r[1])
		tables[r[0]] = tbl
	}

	for _, r := range query(t, db, `
SELECT tc.table_name, kcu.column_name
FROM information_schema.table_constraints tc
JOIN information_schema.key_column_usage kcu
  ON kcu.constraint_name = tc.constraint_name AND kcu.table_schema = tc.table_schema
WHERE tc.table_schema = 'public' AND tc.constraint_type = 'PRIMARY KEY'
ORDER BY tc.table_name, kcu.ordinal_position`) {
		if tbl, ok := tables[r[0]]; ok {
			tbl.PrimaryKey = append(tbl.PrimaryKey, r[1])
			tables[r[0]] = tbl
		}
	}

	indexRows := query(t, db, `
SELECT i.indexname
FROM pg_indexes i
WHERE i.schemaname = 'public' AND i.tablename NOT LIKE 'river_%'
  AND NOT EXISTS (SELECT 1 FROM pg_constraint c WHERE c.conname = i.indexname)
ORDER BY i.indexname`)

	indexes := make([]string, 0, len(indexRows))
	for _, r := range indexRows {
		indexes = append(indexes, r[0])
	}

	return tables, indexes
}

func sqliteSchema(t *testing.T, db *sql.DB) (map[string]table, []string) {
	t.Helper()

	tables := map[string]table{}
	for _, r := range query(t, db, `
SELECT name FROM sqlite_schema
WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name NOT LIKE 'river_%' AND name <> 'goose_db_version'`) {
		tbl := tableOf(tables, r[0])

		cols := query(
			t,
			db,
			`SELECT name, type, "notnull", dflt_value IS NOT NULL, pk FROM pragma_table_info(?) ORDER BY cid`,
			r[0],
		)
		for _, c := range cols {
			tbl.Order = append(tbl.Order, c[0])
		}

		slices.SortFunc(cols, func(a, b []string) int {
			return strings.Compare(a[4], b[4])
		})

		for _, c := range cols {
			tbl.Columns[c[0]] = column{
				Kind:       sqliteKind(c[1]),
				NotNull:    c[2] == "1",
				HasDefault: c[3] == "1",
			}

			if c[4] != "0" {
				tbl.PrimaryKey = append(tbl.PrimaryKey, c[0])
			}
		}

		tables[r[0]] = tbl
	}

	indexRows := query(t, db, `
SELECT name FROM sqlite_schema
WHERE type = 'index' AND sql IS NOT NULL AND name NOT LIKE 'river_%'
ORDER BY name`)

	indexes := make([]string, 0, len(indexRows))
	for _, r := range indexRows {
		indexes = append(indexes, r[0])
	}

	return tables, indexes
}

func tableOf(tables map[string]table, name string) table {
	tbl, ok := tables[name]
	if !ok {
		tbl = table{
			Columns: map[string]column{},
		}
		tables[name] = tbl
	}

	return tbl
}

func postgresqlKind(dataType string) string {
	switch dataType {
	case "uuid", "text", "json", "jsonb", "ARRAY":
		return "text"
	case "timestamp with time zone":
		return "timestamp"
	case "bigint", "integer":
		return "integer"
	case "double precision":
		return "real"
	case "bytea":
		return "blob"
	default:
		return dataType
	}
}

func sqliteKind(declared string) string {
	return strings.ToLower(declared)
}

func query(t *testing.T, db *sql.DB, q string, args ...any) [][]string {
	t.Helper()

	rows, err := db.QueryContext(t.Context(), q, args...)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}

	defer func() {
		_ = rows.Close()
	}()

	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}

	var out [][]string

	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))

		for i := range vals {
			ptrs[i] = &vals[i]
		}

		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}

		row := make([]string, len(cols))

		for i, v := range vals {
			switch v := v.(type) {
			case []byte:
				row[i] = string(v)
			case bool:
				row[i] = strconv.FormatBool(v)
			default:
				row[i] = fmt.Sprint(v)
			}
		}

		out = append(out, row)
	}

	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	return out
}

func versions(t *testing.T, tree fs.FS) []int64 {
	t.Helper()

	files, err := fs.Glob(tree, "*.sql")
	if err != nil {
		t.Fatal(err)
	}

	out := make([]int64, 0, len(files))
	for _, f := range files {
		v, err := strconv.ParseInt(strings.SplitN(f, "_", 2)[0], 10, 64)
		if err != nil {
			t.Fatalf("migration %s: %v", f, err)
		}

		out = append(out, v)
	}

	slices.Sort(out)

	return out
}
