package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"relic/internal/platform/localdisk"
	"relic/internal/platform/telemetry"
	"relic/migrations"

	"github.com/pressly/goose/v3"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riversqlite"
	"github.com/riverqueue/river/rivermigrate"
	_ "modernc.org/sqlite"
)

type sqliteEngine struct {
	path       string
	db         *sql.DB
	jobs       *sql.DB
	traced     sqliteDBTX
	unregister func()
}

const sqliteMaxConnections = 8

const sqliteBusyTimeout = 10 * time.Second

func openSQLite(ctx context.Context, path string) (*sqliteEngine, Querier, error) {
	if !filepath.IsAbs(path) {
		return nil, nil, fmt.Errorf("sqlite database path %q must be absolute (sqlite:///path/to/relic.db)", path)
	}

	if err := localdisk.Check(filepath.Dir(path)); err != nil {
		return nil, nil, err
	}

	dsn := sqliteDSN(path)

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("open database: %w", err)
	}

	db.SetMaxOpenConns(sqliteMaxConnections)

	jobs, err := sql.Open("sqlite", dsn)
	if err != nil {
		_ = db.Close()

		return nil, nil, fmt.Errorf("open database: %w", err)
	}

	jobs.SetMaxOpenConns(1)

	acquired, unregisterDB := telemetry.RegisterSQLDB(db)
	unregisterDisk := telemetry.RegisterDisk(
		telemetry.VolumeDatabase,
		func() (uint64, uint64, error) {
			return localdisk.Usage(filepath.Dir(path))
		},
		func() (int64, error) {
			return fileSizes(path, path+"-wal")
		},
	)

	e := &sqliteEngine{
		path: path,
		db:   db,
		jobs: jobs,
		traced: tracedSQLite{
			db:       db,
			acquired: acquired,
		},
		unregister: func() {
			unregisterDB()
			unregisterDisk()
		},
	}

	if err := e.ping(ctx); err != nil {
		e.close()

		return nil, nil, fmt.Errorf("ping database: %w", err)
	}

	return e, newSQLiteQuerier(e.traced), nil
}

func fileSizes(paths ...string) (int64, error) {
	var n int64

	for _, p := range paths {
		st, err := os.Stat(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}

		if err != nil {
			return 0, err
		}

		n += st.Size()
	}

	return n, nil
}

func sqliteDSN(path string) string {
	q := url.Values{}
	for _, p := range []string{
		"foreign_keys(1)",
		"journal_mode(WAL)",
		"synchronous(FULL)",
		fmt.Sprintf("busy_timeout(%d)", sqliteBusyTimeout.Milliseconds()),
		"case_sensitive_like(1)",
	} {
		q.Add("_pragma", p)
	}

	q.Set("_txlock", "immediate")

	file := url.URL{
		Path: path,
	}

	return "file:" + file.EscapedPath() + "?" + q.Encode()
}

func (e *sqliteEngine) close() {
	e.unregister()
	_ = e.jobs.Close()
	_ = e.db.Close()
}

func (e *sqliteEngine) ping(ctx context.Context) error {
	return e.db.PingContext(ctx)
}

func (e *sqliteEngine) withTx(ctx context.Context, fn func(q Querier) error) (err error) {
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}

	defer func() {
		if err != nil {
			err = rollback(err, tx.Rollback)
		}
	}()

	if err := fn(newSQLiteQuerier(tracedSQLite{
		db: tx,
	})); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	return nil
}

func (e *sqliteEngine) query(ctx context.Context, query string, args ...any) (Rows, error) {
	bound := make([]any, len(args))
	for i, a := range args {
		switch v := a.(type) {
		case time.Time:
			bound[i] = sqliteTime(v)
		case *time.Time:
			bound[i] = sqliteNullTime(v)
		default:
			bound[i] = a
		}
	}

	//nolint:rowserrcheck,sqlclosecheck // returned to the caller, which checks and closes them
	rows, err := e.traced.QueryContext(ctx, query, bound...)
	if err != nil {
		return nil, err
	}

	return sqliteRows{
		rows,
	}, nil
}

type tracedSQLite struct {
	db       sqliteDBTX
	acquired func()
}

func (t tracedSQLite) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	ctx, end := telemetry.ObserveQuery(ctx, "sqlite", query)

	if t.acquired != nil {
		t.acquired()
	}

	res, err := t.db.ExecContext(ctx, query, args...)
	end(err)

	return res, err
}

func (t tracedSQLite) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	ctx, end := telemetry.ObserveQuery(ctx, "sqlite", query)

	if t.acquired != nil {
		t.acquired()
	}

	rows, err := t.db.QueryContext(ctx, query, args...)
	end(err)

	return rows, err
}

type sqliteRows struct {
	*sql.Rows
}

func (r sqliteRows) Scan(dest ...any) error {
	targets := make([]any, len(dest))
	for i, d := range dest {
		switch v := d.(type) {
		case *time.Time:
			targets[i] = sqliteTimeScanner{
				v,
			}
		case **time.Time:
			targets[i] = sqliteNullTimeScanner{
				v,
			}
		default:
			targets[i] = d
		}
	}

	return r.Rows.Scan(targets...)
}

func (r sqliteRows) Close() {
	_ = r.Rows.Close()
}

func (e *sqliteEngine) migrateTo(ctx context.Context, version int64) error {
	unlock, err := localdisk.Lock(e.path + ".migrate-lock")
	if err != nil {
		return fmt.Errorf("migration lock: %w", err)
	}
	defer unlock()

	provider, err := goose.NewProvider(goose.DialectSQLite3, e.db, migrations.SQLite)
	if err != nil {
		return fmt.Errorf("migrations: %w", err)
	}

	if _, err := provider.UpTo(ctx, version); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	return nil
}

func (e *sqliteEngine) migrateJobQueue(ctx context.Context) error {
	unlock, err := localdisk.Lock(e.path + ".migrate-lock")
	if err != nil {
		return fmt.Errorf("job queue migration lock: %w", err)
	}
	defer unlock()

	migrator, err := rivermigrate.New(riversqlite.New(e.jobs), nil)
	if err != nil {
		return fmt.Errorf("job queue migrations: %w", err)
	}

	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return fmt.Errorf("migrate job queue: %w", err)
	}

	return nil
}

func (e *sqliteEngine) newJobClient(cfg *river.Config) (JobClient, error) {
	client, err := river.NewClient(riversqlite.New(e.jobs), cfg)
	if err != nil {
		return nil, fmt.Errorf("job client: %w", err)
	}

	return client, nil
}

func (e *sqliteEngine) jobQueries() jobQueries {
	return jobQueries{
		stats: `
SELECT kind, state, count(*), max(finalized_at)
FROM river_job
GROUP BY kind, state
ORDER BY kind, state`,
		queues: `
SELECT kind,
       count(*) FILTER (WHERE state = 'available'),
       count(*) FILTER (WHERE state = 'retryable'),
       min(scheduled_at) FILTER (WHERE state = 'available')
FROM river_job
WHERE state IN ('available', 'retryable')
GROUP BY kind
ORDER BY kind`,
		lastErrors: `
SELECT kind, error, at
FROM (
  SELECT kind,
         json_extract(json(errors), '$[#-1].error') AS error,
         json_extract(json(errors), '$[#-1].at') AS at,
         row_number() OVER (
           PARTITION BY kind ORDER BY unixepoch(json_extract(json(errors), '$[#-1].at'), 'subsec') DESC
         ) AS rn
  FROM river_job
  WHERE errors IS NOT NULL AND json_array_length(json(errors)) > 0
)
WHERE rn = 1
ORDER BY kind`,
		workers: `
SELECT json_extract(json(attempted_by), '$[#-1]') AS id,
       count(*) FILTER (WHERE state = 'running'),
       max(attempted_at)
FROM river_job
WHERE attempted_by IS NOT NULL AND json_array_length(json(attempted_by)) > 0
  AND (state = 'running' OR unixepoch(attempted_at) > unixepoch('now', '-1 hour'))
GROUP BY 1
ORDER BY 1`,
		leader: `SELECT leader_id FROM river_leader WHERE unixepoch(expires_at, 'subsec') > unixepoch('now', 'subsec') LIMIT 1`,
		queueStatuses: `
SELECT q.name, q.updated_at, q.paused_at IS NOT NULL,
       count(j.id) FILTER (WHERE j.state IN ('available', 'retryable', 'scheduled')),
       count(j.id) FILTER (WHERE j.state = 'running')
FROM river_queue q
LEFT JOIN river_job j ON j.queue = q.name AND j.state IN ('available', 'retryable', 'scheduled', 'running')
GROUP BY q.name, q.updated_at, q.paused_at
ORDER BY q.name`,
		jobs: `
SELECT id, kind, state, queue, attempt, max_attempts, created_at, scheduled_at, attempted_at, finalized_at,
       json_extract(json(attempted_by), '$[#-1]'), json(args), coalesce(json(errors), '[]'),
       coalesce(json_extract(json(metadata), '$.output'), 'null')
FROM river_job
WHERE (?1 = '' OR kind = ?1) AND (?2 = '' OR state = ?2)
ORDER BY id DESC
LIMIT ?3`,
	}
}
