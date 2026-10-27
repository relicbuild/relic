package database

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"relic/internal/platform/localdisk"
)

const fullDiskWindow = time.Minute

type fullDisk struct {
	refusedAt atomic.Int64
}

func (f *fullDisk) mark(err error) error {
	if !isFull(err) {
		return err
	}

	f.refusedAt.Store(time.Now().UnixNano())

	if errors.Is(err, localdisk.ErrFull) {
		return err
	}

	return fmt.Errorf("%w: %w", localdisk.ErrFull, err)
}

func (f *fullDisk) refusedSince(since time.Time) bool {
	refusedAt := f.refusedAt.Load()

	return refusedAt != 0 && refusedAt >= since.UnixNano()
}

func isFull(err error) bool {
	if err == nil {
		return false
	}

	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		return pgErr.Code == "53100" || pgErr.Code == "53000"
	}

	liteErr, ok := errors.AsType[*sqlite.Error](err)

	return ok && liteErr.Code()&0xff == sqlite3.SQLITE_FULL
}

type fullMarkingPostgreSQL struct {
	db   DBTX
	full *fullDisk
}

func (p fullMarkingPostgreSQL) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tag, err := p.db.Exec(ctx, sql, args...)

	return tag, p.full.mark(err)
}

func (p fullMarkingPostgreSQL) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	rows, err := p.db.Query(ctx, sql, args...)
	if err != nil {
		return rows, p.full.mark(err)
	}

	return fullMarkingRows{
		Rows: rows,
		full: p.full,
	}, nil
}

func (p fullMarkingPostgreSQL) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return fullMarkingRow{
		row:  p.db.QueryRow(ctx, sql, args...),
		full: p.full,
	}
}

type fullMarkingRows struct {
	pgx.Rows

	full *fullDisk
}

func (r fullMarkingRows) Err() error { return r.full.mark(r.Rows.Err()) }

type fullMarkingRow struct {
	row  pgx.Row
	full *fullDisk
}

func (r fullMarkingRow) Scan(dest ...any) error { return r.full.mark(r.row.Scan(dest...)) }

// Ready checks that the database answers and has not refused a write for lack
// of space (localdisk.ErrFull) within the last minute.
func (db *DB) Ready(ctx context.Context) error {
	if err := db.Ping(ctx); err != nil {
		return err
	}

	if db.full.refusedSince(time.Now().Add(-fullDiskWindow)) {
		return fmt.Errorf("the database refused a write for lack of space: %w", localdisk.ErrFull)
	}

	return nil
}
