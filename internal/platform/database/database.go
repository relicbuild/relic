package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"net"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"
	"github.com/riverqueue/river"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"relic/internal/platform/telemetry"
)

// ErrNotFound is returned by lookups that match no row.
var ErrNotFound = errors.New("not found")

// DB is the metadata store: the queries, transactions and the job queue, on
// PostgreSQL or SQLite.
type DB struct {
	Querier
	engine engine
}

type engine interface {
	withTx(ctx context.Context, fn func(q Querier) error) error
	withLock(ctx context.Context, id int64, fn func() error) error
	query(ctx context.Context, sql string, args ...any) (Rows, error)
	ping(ctx context.Context) error
	close()
	migrateTo(ctx context.Context, version int64) ([]Migration, error)
	migrateJobQueue(ctx context.Context) error
	newJobClient(cfg *river.Config) (JobClient, error)
	jobQueries() jobQueries
}

// Open connects to the database url names and verifies the connection:
// PostgreSQL for postgres:// and postgresql:// URLs, SQLite for
// sqlite:///absolute/path/to/relic.db.
func Open(ctx context.Context, url string) (*DB, error) {
	return OpenWith(ctx, url, Options{})
}

// Options tune a connection pool.
type Options struct {
	StatementTimeout time.Duration
	MaxConnections   int32
	// MustExist refuses a SQLite database file that does not exist instead
	// of creating an empty one.
	MustExist bool
}

// OpenWith is Open with options.
func OpenWith(ctx context.Context, url string, opts Options) (*DB, error) {
	if path, ok := strings.CutPrefix(url, "sqlite://"); ok {
		e, q, err := openSQLite(ctx, path, opts.MustExist)
		if err != nil {
			return nil, err
		}

		return &DB{
			Querier: q,
			engine:  e,
		}, nil
	}

	e, err := openPostgreSQL(ctx, url, opts)
	if err != nil {
		return nil, err
	}

	return &DB{
		Querier: e.queries,
		engine:  e,
	}, nil
}

// Close releases all connections.
func (db *DB) Close() { db.engine.close() }

// Ping checks the database is reachable.
func (db *DB) Ping(ctx context.Context) error { return db.engine.ping(ctx) }

// Migration is a schema migration that Migrate applied.
type Migration struct {
	Version  int64
	Name     string
	Duration time.Duration
}

// Migrate applies all pending migrations, Relic's and the job queue's, and
// returns Relic's that it applied.
func (db *DB) Migrate(ctx context.Context) ([]Migration, error) {
	applied, err := db.engine.migrateTo(ctx, math.MaxInt64)
	if err != nil {
		return nil, err
	}

	return applied, db.engine.migrateJobQueue(ctx)
}

// MigrateTo applies the migrations up to version only.
func (db *DB) MigrateTo(ctx context.Context, version int64) ([]Migration, error) {
	return db.engine.migrateTo(ctx, version)
}

// NewerSchemaError is returned by Migrate when a newer Relic migrated the
// database past the newest migration this binary has.
type NewerSchemaError struct {
	Database int64
	Binary   int64
}

func (e *NewerSchemaError) Error() string {
	return fmt.Sprintf(
		"the database schema is at version %d, newer than version %d, the newest this binary knows: "+
			"run the Relic release that migrated it, or restore the backup taken before the upgrade",
		e.Database,
		e.Binary,
	)
}

func migrateUpTo(ctx context.Context, provider *goose.Provider, version int64) ([]Migration, error) {
	current, err := provider.GetDBVersion(ctx)
	if err != nil {
		return nil, fmt.Errorf("schema version: %w", err)
	}

	if sources := provider.ListSources(); len(sources) > 0 && current > sources[len(sources)-1].Version {
		return nil, &NewerSchemaError{
			Database: current,
			Binary:   sources[len(sources)-1].Version,
		}
	}

	results, err := provider.UpTo(ctx, version)
	if err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}

	applied := make([]Migration, 0, len(results))
	for _, r := range results {
		applied = append(applied, Migration{
			Version:  r.Source.Version,
			Name:     strings.TrimSuffix(path.Base(r.Source.Path), path.Ext(r.Source.Path)),
			Duration: r.Duration,
		})
	}

	return applied, nil
}

// ErrRetryTransaction is returned by a WithTx function to run it again.
var ErrRetryTransaction = errors.New("transaction conflicted with a concurrent one")

const transactionAttempts = 4

// WithTx runs fn in a transaction, committing if fn returns nil, and runs it
// again after a deadlock, a serialization failure, a busy SQLite database or
// ErrRetryTransaction; fn must be safe to run more than once.
func (db *DB) WithTx(ctx context.Context, fn func(q Querier) error) error {
	for attempt := 1; ; attempt++ {
		err := db.engine.withTx(ctx, fn)

		reason := retryReason(err)
		if reason == "" || attempt == transactionAttempts {
			return err
		}

		telemetry.DBTransactionRetried(reason)

		select {
		case <-ctx.Done():
			return errors.Join(ctx.Err(), err)
		case <-time.After(retryDelay(attempt)):
		}
	}
}

func retryReason(err error) string {
	if err == nil {
		return ""
	}

	if errors.Is(err, ErrRetryTransaction) {
		return telemetry.RetryConflict
	}

	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		switch pgErr.Code {
		case "40P01":
			return telemetry.RetryDeadlock
		case "40001":
			return telemetry.RetrySerialization
		}

		return ""
	}

	if liteErr, ok := errors.AsType[*sqlite.Error](err); ok && liteErr.Code()&0xff == sqlite3.SQLITE_BUSY {
		return telemetry.RetryBusy
	}

	return ""
}

func retryDelay(attempt int) time.Duration {
	base := time.Duration(attempt) * 10 * time.Millisecond

	return base + rand.N(3*base) //nolint:gosec // jitter, not a secret
}

// Query runs a dynamically planned, parameterized query.
// Callers must build sql from allowlisted fragments only.
func (db *DB) Query(ctx context.Context, sql string, args ...any) (Rows, error) {
	return db.engine.query(ctx, sql, args...)
}

// TruncateLog moves a SQLite database's write-ahead log into the database
// file and empties it, so that page images it held, such as a row's earlier
// text, are gone; on PostgreSQL it does nothing.
func (db *DB) TruncateLog(ctx context.Context) error {
	e, ok := db.engine.(*sqliteEngine)
	if !ok {
		return nil
	}

	if _, err := e.writer.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return fmt.Errorf("truncate the write-ahead log: %w", err)
	}

	return nil
}

// SQLite reports whether the database is SQLite rather than PostgreSQL.
func (db *DB) SQLite() bool {
	_, ok := db.engine.(*sqliteEngine)

	return ok
}

// Pool exposes the PostgreSQL connection pool to tests that run SQL of their
// own.
func (db *DB) Pool() *pgxpool.Pool {
	if e, ok := db.engine.(*postgresEngine); ok {
		return e.pool
	}

	return nil
}

// NotFound maps a query's "no rows" error to ErrNotFound, leaving other
// errors intact.
func NotFound(err error) error {
	if IsNoRows(err) {
		return ErrNotFound
	}

	return err
}

// IsNoRows reports whether err is a single-row query that matched no row.
func IsNoRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows) || errors.Is(err, sql.ErrNoRows)
}

// IsUnavailable reports whether err means the database could not be reached
// or dropped the connection, rather than refusing the statement.
func IsUnavailable(err error) bool {
	if _, ok := errors.AsType[*pgconn.ConnectError](err); ok {
		return true
	}

	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		return strings.HasPrefix(pgErr.Code, "08") || slices.Contains([]string{
			"57P01",
			"57P02",
			"57P03",
		}, pgErr.Code)
	}

	if _, ok := errors.AsType[*net.OpError](err); ok {
		return true
	}

	liteErr, ok := errors.AsType[*sqlite.Error](err)

	return ok && liteErr.Code()&0xff == sqlite3.SQLITE_CANTOPEN
}

// IsUniqueViolation reports whether err is a unique-constraint error.
func IsUniqueViolation(err error) bool {
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		return pgErr.Code == "23505"
	}

	if liteErr, ok := errors.AsType[*sqlite.Error](err); ok {
		code := liteErr.Code()

		return code == sqlite3.SQLITE_CONSTRAINT_UNIQUE || code == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY
	}

	return false
}

// Rows are the rows of a planned query (Query).
type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close()
}

func rollback(err error, rb func() error) error {
	rbErr := rb()
	if rbErr == nil || errors.Is(rbErr, pgx.ErrTxClosed) || errors.Is(rbErr, sql.ErrTxDone) {
		return err
	}

	return errors.Join(err, fmt.Errorf("rollback: %w", rbErr))
}
