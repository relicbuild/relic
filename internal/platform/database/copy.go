package database

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

// ErrTargetNotEmpty is returned by Copy when the target database holds data.
var ErrTargetNotEmpty = errors.New("the target database is not empty")

// ErrBackupNotSupported is returned by Backup on PostgreSQL.
var ErrBackupNotSupported = errors.New("relic backs up SQLite databases only; back up PostgreSQL with pg_dump")

const (
	copyBatch      = 500
	copyBatchBytes = 64 << 20
)

func copyBatchFull(rows, size int) bool {
	return rows >= copyBatch || size >= copyBatchBytes
}

type columnKind int

const (
	kindText columnKind = iota
	kindInteger
	kindReal
	kindBool
	kindTimestamp
	kindDate
	kindBytes
	kindJSON
)

type column struct {
	name string
	kind columnKind
}

// CopyProgress reports a table the copy finished and its rows.
type CopyProgress func(table string, rows int64)

// Copy copies every table of from into to, which must be empty.
func Copy(ctx context.Context, from, to *DB, progress CopyProgress) error {
	if err := to.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate the target: %w", err)
	}

	tables, err := to.copyTables(ctx)
	if err != nil {
		return err
	}

	for _, t := range tables {
		n, err := to.count(ctx, t)
		if err != nil {
			return err
		}

		if n > 0 {
			return fmt.Errorf("%w: %s has %d rows", ErrTargetNotEmpty, t, n)
		}
	}

	kinds := to
	if _, ok := from.engine.(*postgresEngine); ok {
		kinds = from
	}

	err = to.copyTx(ctx, func(insert func(table string, cols []column, batch string) error) error {
		for _, t := range tables {
			cols, err := kinds.columns(ctx, t)
			if err != nil {
				return err
			}

			n, err := from.readRows(ctx, t, cols, func(batch string) error {
				return insert(t, cols, batch)
			})
			if err != nil {
				return fmt.Errorf("copy %s: %w", t, err)
			}

			if progress != nil {
				progress(t, n)
			}
		}

		return nil
	})
	if err != nil {
		return err
	}

	for _, t := range tables {
		want, err := from.count(ctx, t)
		if err != nil {
			return err
		}

		got, err := to.count(ctx, t)
		if err != nil {
			return err
		}

		if got != want {
			return fmt.Errorf("copy %s: %d rows in the source, %d in the target", t, want, got)
		}
	}

	return nil
}

// Backup writes a consistent snapshot of a SQLite database to path, which
// must not exist.
func Backup(ctx context.Context, db *DB, path string) error {
	e, ok := db.engine.(*sqliteEngine)
	if !ok {
		return ErrBackupNotSupported
	}

	if err := createOwnerOnly(path); errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("backup %s: the file exists", path)
	} else if err != nil {
		return fmt.Errorf("backup %s: %w", path, err)
	}

	if _, err := e.db.ExecContext(ctx, "VACUUM INTO ?", path); err != nil {
		return errors.Join(fmt.Errorf("backup %s: %w", path, err), os.Remove(path))
	}

	return nil
}

func (db *DB) copyTables(ctx context.Context) ([]string, error) {
	var (
		names []string
		refs  = map[string][]string{}
	)

	switch e := db.engine.(type) {
	case *postgresEngine:
		rows, err := e.pool.Query(ctx, `
SELECT c.relname, coalesce(r.relname, '')
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
LEFT JOIN pg_constraint k ON k.conrelid = c.oid AND k.contype = 'f'
LEFT JOIN pg_class r ON r.oid = k.confrelid
WHERE c.relkind = 'r'`)
		if err != nil {
			return nil, fmt.Errorf("list tables: %w", err)
		}

		pairs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[struct {
			Table, Ref string
		}])
		if err != nil {
			return nil, fmt.Errorf("list tables: %w", err)
		}

		for _, p := range pairs {
			names = append(names, p.Table)
			if p.Ref != "" && p.Ref != p.Table {
				refs[p.Table] = append(refs[p.Table], p.Ref)
			}
		}
	case *sqliteEngine:
		rows, err := e.db.QueryContext(ctx, `
SELECT m.name, coalesce(f."table", '')
FROM sqlite_schema m
LEFT JOIN pragma_foreign_key_list(m.name) f
WHERE m.type = 'table'`)
		if err != nil {
			return nil, fmt.Errorf("list tables: %w", err)
		}

		defer func() {
			_ = rows.Close()
		}()

		for rows.Next() {
			var table, ref string
			if err := rows.Scan(&table, &ref); err != nil {
				return nil, fmt.Errorf("list tables: %w", err)
			}

			names = append(names, table)
			if ref != "" && ref != table {
				refs[table] = append(refs[table], ref)
			}
		}

		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("list tables: %w", err)
		}
	}

	slices.Sort(names)
	names = slices.Compact(names)
	names = slices.DeleteFunc(names, func(n string) bool {
		return n == "goose_db_version" || strings.HasPrefix(n, "river_") || strings.HasPrefix(n, "sqlite_")
	})

	return dependencyOrder(names, refs)
}

func dependencyOrder(names []string, refs map[string][]string) ([]string, error) {
	out := make([]string, 0, len(names))
	done := map[string]bool{}

	for len(out) < len(names) {
		progressed := false

		for _, n := range names {
			if done[n] {
				continue
			}

			ready := true

			for _, r := range refs[n] {
				if !done[r] && slices.Contains(names, r) {
					ready = false
				}
			}

			if ready {
				out = append(out, n)
				done[n] = true
				progressed = true
			}
		}

		if !progressed {
			return nil, errors.New("list tables: the foreign keys form a cycle")
		}
	}

	return out, nil
}

func (db *DB) columns(ctx context.Context, table string) ([]column, error) {
	var cols []column

	switch e := db.engine.(type) {
	case *postgresEngine:
		rows, err := e.pool.Query(ctx, `
SELECT column_name, data_type
FROM information_schema.columns
WHERE table_schema = 'public' AND table_name = $1
ORDER BY ordinal_position`, table)
		if err != nil {
			return nil, fmt.Errorf("columns of %s: %w", table, err)
		}

		pairs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[struct {
			Name, Type string
		}])
		if err != nil {
			return nil, fmt.Errorf("columns of %s: %w", table, err)
		}

		for _, p := range pairs {
			cols = append(cols, column{
				name: p.Name,
				kind: postgresqlKind(p.Type),
			})
		}
	case *sqliteEngine:
		rows, err := e.db.QueryContext(ctx, `SELECT name, type FROM pragma_table_info(?) ORDER BY cid`, table)
		if err != nil {
			return nil, fmt.Errorf("columns of %s: %w", table, err)
		}

		defer func() {
			_ = rows.Close()
		}()

		for rows.Next() {
			var name, decl string
			if err := rows.Scan(&name, &decl); err != nil {
				return nil, fmt.Errorf("columns of %s: %w", table, err)
			}

			cols = append(cols, column{
				name: name,
				kind: sqliteKind(decl),
			})
		}

		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("columns of %s: %w", table, err)
		}
	}

	if len(cols) == 0 {
		return nil, fmt.Errorf("table %s has no columns", table)
	}

	return cols, nil
}

func postgresqlKind(dataType string) columnKind {
	switch dataType {
	case "bigint", "integer", "smallint":
		return kindInteger
	case "double precision", "real", "numeric":
		return kindReal
	case "boolean":
		return kindBool
	case "timestamp with time zone", "timestamp without time zone":
		return kindTimestamp
	case "date":
		return kindDate
	case "bytea":
		return kindBytes
	case "json", "jsonb", "ARRAY":
		return kindJSON
	default:
		return kindText
	}
}

func sqliteKind(decl string) columnKind {
	switch strings.ToLower(decl) {
	case "integer":
		return kindInteger
	case "real":
		return kindReal
	case "boolean":
		return kindBool
	case "timestamp":
		return kindTimestamp
	case "date":
		return kindDate
	case "blob":
		return kindBytes
	default:
		return kindText
	}
}

func (db *DB) readRows(ctx context.Context, table string, cols []column, fn func(batch string) error) (int64, error) {
	query := db.selectRows(table, cols)

	var (
		n          int64
		batch      []string
		batchBytes int
	)

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}

		err := fn("[" + strings.Join(batch, ",") + "]")
		batch, batchBytes = batch[:0], 0

		return err
	}

	rows, err := db.engine.query(ctx, query)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	for rows.Next() {
		var row string
		if err := rows.Scan(&row); err != nil {
			return n, err
		}

		batch = append(batch, row)
		batchBytes += len(row)
		n++

		if copyBatchFull(len(batch), batchBytes) {
			if err := flush(); err != nil {
				return n, err
			}
		}
	}

	if err := rows.Err(); err != nil {
		return n, err
	}

	return n, flush()
}

func (db *DB) selectRows(table string, cols []column) string {
	pairs := make([]string, 0, 2*len(cols))

	if _, ok := db.engine.(*postgresEngine); ok {
		for _, c := range cols {
			col := pgx.Identifier{
				c.name,
			}.Sanitize()

			expr := col

			switch c.kind {
			case kindTimestamp:
				expr = "to_char(" + col + " AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US') || '+00:00'"
			case kindDate:
				expr = "to_char(" + col + ", 'YYYY-MM-DD')"
			case kindBytes:
				expr = "'\\x' || encode(" + col + ", 'hex')"
			case kindText, kindInteger, kindReal, kindBool, kindJSON:
			}

			pairs = append(pairs, quoteLiteral(c.name), expr)
		}

		return "SELECT jsonb_build_object(" + strings.Join(pairs, ", ") + ")::text FROM " +
			pgx.Identifier{
				table,
			}.Sanitize()
	}

	for _, c := range cols {
		col := `"` + strings.ReplaceAll(c.name, `"`, `""`) + `"`
		expr := col

		switch c.kind {
		case kindBytes:
			expr = "CASE WHEN " + col + " IS NULL THEN NULL ELSE '\\x' || lower(hex(" + col + ")) END"
		case kindBool:
			expr = "CASE WHEN " + col + " IS NULL THEN NULL WHEN " + col + " THEN json('true') ELSE json('false') END"
		case kindJSON:
			expr = "json(" + col + ")"
		case kindText, kindInteger, kindReal, kindTimestamp, kindDate:
		}

		pairs = append(pairs, quoteLiteral(c.name), expr)
	}

	return `SELECT json_object(` + strings.Join(pairs, ", ") + `) FROM "` + strings.ReplaceAll(table, `"`, `""`) + `"`
}

func (db *DB) copyTx(
	ctx context.Context,
	fn func(insert func(table string, cols []column, batch string) error) error,
) (err error) {
	switch e := db.engine.(type) {
	case *postgresEngine:
		tx, beginErr := e.pool.Begin(ctx)
		if beginErr != nil {
			return fmt.Errorf("begin: %w", beginErr)
		}

		defer func() {
			if err != nil {
				err = rollback(err, func() error {
					return tx.Rollback(context.WithoutCancel(ctx))
				})
			}
		}()

		err = fn(func(table string, _ []column, batch string) error {
			t := pgx.Identifier{
				table,
			}.Sanitize()
			_, err := tx.Exec(
				ctx,
				"INSERT INTO "+t+" SELECT * FROM jsonb_populate_recordset(NULL::"+t+", $1::jsonb)",
				batch,
			)

			return err
		})
		if err != nil {
			return err
		}

		return tx.Commit(ctx)
	case *sqliteEngine:
		tx, beginErr := e.db.BeginTx(ctx, nil)
		if beginErr != nil {
			return fmt.Errorf("begin: %w", beginErr)
		}

		defer func() {
			if err != nil {
				err = rollback(err, tx.Rollback)
			}
		}()

		err = fn(func(table string, cols []column, batch string) error {
			_, err := tx.ExecContext(ctx, sqliteInsertRows(table, cols), batch)

			return err
		})
		if err != nil {
			return err
		}

		return tx.Commit()
	default:
		return errors.New("unknown database engine")
	}
}

func sqliteInsertRows(table string, cols []column) string {
	names := make([]string, 0, len(cols))
	exprs := make([]string, 0, len(cols))

	for _, c := range cols {
		names = append(names, `"`+strings.ReplaceAll(c.name, `"`, `""`)+`"`)

		path := quoteLiteral(`$."` + c.name + `"`)
		value := "j.value ->> " + path

		switch c.kind {
		case kindBytes:
			value = "unhex(substr(" + value + ", 3))"
		case kindJSON:
			value = "CASE WHEN coalesce(json_type(j.value, " + path + "), 'null') = 'null' THEN NULL ELSE j.value -> " + path + " END"
		case kindText, kindInteger, kindReal, kindBool, kindTimestamp, kindDate:
		}

		exprs = append(exprs, value)
	}

	return `INSERT INTO "` + strings.ReplaceAll(table, `"`, `""`) + `" (` + strings.Join(names, ", ") +
		") SELECT " + strings.Join(exprs, ", ") + " FROM json_each(?1) AS j"
}

func (db *DB) count(ctx context.Context, table string) (int64, error) {
	var q string
	if _, ok := db.engine.(*postgresEngine); ok {
		q = "SELECT count(*) FROM " + pgx.Identifier{
			table,
		}.Sanitize()
	} else {
		q = `SELECT count(*) FROM "` + strings.ReplaceAll(table, `"`, `""`) + `"`
	}

	rows, err := db.engine.query(ctx, q)
	if err != nil {
		return 0, fmt.Errorf("count %s: %w", table, err)
	}
	defer rows.Close()

	var n int64
	if rows.Next() {
		if err := rows.Scan(&n); err != nil {
			return 0, fmt.Errorf("count %s: %w", table, err)
		}
	}

	return n, rows.Err()
}

func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
