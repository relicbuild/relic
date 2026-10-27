package database

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"relic/internal/platform/telemetry"
	"relic/migrations"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

type postgresEngine struct {
	pool          *pgxpool.Pool
	queries       *Queries
	unregister    func()
	advisoryLocks *advisoryLocks
	localLocks    *localLocks
	full          *fullDisk
}

func openPostgreSQL(ctx context.Context, url string, opts Options, full *fullDisk) (*postgresEngine, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}

	if opts.MaxConnections > 0 && !strings.Contains(url, "pool_max_conns=") {
		cfg.MaxConns = opts.MaxConnections
	}

	statementTimeout := opts.StatementTimeout
	cfg.MaxConnLifetime = 30 * time.Minute

	cfg.MaxConnIdleTime = 5 * time.Minute
	if statementTimeout > 0 {
		cfg.ConnConfig.RuntimeParams["statement_timeout"] = strconv.FormatInt(statementTimeout.Milliseconds(), 10)
	}

	cfg.ConnConfig.Tracer = telemetry.DBTracer()

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()

		return nil, fmt.Errorf("ping database: %w", err)
	}

	return &postgresEngine{
		pool: pool,
		queries: New(fullMarkingPostgreSQL{
			db:   pool,
			full: full,
		}),
		unregister: telemetry.RegisterPool(pool),
		advisoryLocks: &advisoryLocks{
			config: cfg.ConnConfig.Copy(),
		},
		localLocks: newLocalLocks(),
		full:       full,
	}, nil
}

func (e *postgresEngine) close() {
	e.unregister()
	e.advisoryLocks.close()
	e.pool.Close()
}

func (e *postgresEngine) ping(ctx context.Context) error { return e.pool.Ping(ctx) }

func (e *postgresEngine) withTx(ctx context.Context, fn func(q Querier) error) (err error) {
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}

	defer func() {
		if err != nil {
			err = rollback(err, func() error {
				return tx.Rollback(context.WithoutCancel(ctx))
			})
		}
	}()

	if err := fn(New(fullMarkingPostgreSQL{
		db:   tx,
		full: e.full,
	})); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", e.full.mark(err))
	}

	return nil
}

func (e *postgresEngine) query(ctx context.Context, sql string, args ...any) (Rows, error) {
	//nolint:sqlclosecheck // returned to the caller, which closes them
	return fullMarkingPostgreSQL{
		db:   e.pool,
		full: e.full,
	}.Query(ctx, sql, args...)
}

func (e *postgresEngine) migrateTo(ctx context.Context, version int64) ([]Migration, error) {
	sqlDB := stdlib.OpenDBFromPool(e.pool)
	defer func() {
		_ = sqlDB.Close()
	}()

	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return nil, fmt.Errorf("migration lock: %w", err)
	}

	provider, err := goose.NewProvider(
		goose.DialectPostgres,
		sqlDB,
		migrations.PostgreSQL,
		goose.WithSessionLocker(locker),
	)
	if err != nil {
		return nil, fmt.Errorf("migrations: %w", err)
	}

	return migrateUpTo(ctx, provider, version)
}

const jobQueueMigrationLock = 0x72656c6963

// The lock is held on a connection of its own outside the pool.
func (e *postgresEngine) migrateJobQueue(ctx context.Context) (err error) {
	conn, err := pgx.ConnectConfig(ctx, e.pool.Config().ConnConfig.Copy())
	if err != nil {
		return fmt.Errorf("job queue migration lock: %w", err)
	}

	defer func() {
		_ = conn.Close(context.WithoutCancel(ctx))
	}()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", jobQueueMigrationLock); err != nil {
		return fmt.Errorf("job queue migration lock: %w", err)
	}

	defer func() {
		if _, uerr := conn.Exec(
			context.WithoutCancel(ctx),
			"SELECT pg_advisory_unlock($1)",
			jobQueueMigrationLock,
		); uerr != nil &&
			err == nil {
			err = fmt.Errorf("job queue migration unlock: %w", uerr)
		}
	}()

	migrator, err := rivermigrate.New(riverpgxv5.New(e.pool), nil)
	if err != nil {
		return fmt.Errorf("job queue migrations: %w", err)
	}

	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return fmt.Errorf("migrate job queue: %w", err)
	}

	return nil
}

func (e *postgresEngine) newJobClient(cfg *river.Config) (JobClient, error) {
	client, err := river.NewClient(riverpgxv5.New(e.pool), cfg)
	if err != nil {
		return nil, fmt.Errorf("job client: %w", err)
	}

	return client, nil
}

func (e *postgresEngine) jobQueries() jobQueries {
	return jobQueries{
		stats: `
SELECT kind, state::text, count(*), max(finalized_at)
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
SELECT DISTINCT ON (kind) kind, e->>'error', (e->>'at')::timestamptz
FROM river_job, LATERAL (SELECT errors[array_length(errors, 1)] AS e) last
WHERE errors IS NOT NULL AND array_length(errors, 1) > 0
ORDER BY kind, (e->>'at')::timestamptz DESC`,
		workers: `
SELECT attempted_by[array_length(attempted_by, 1)] AS id,
       count(*) FILTER (WHERE state = 'running'),
       max(attempted_at)
FROM river_job
WHERE attempted_by IS NOT NULL AND array_length(attempted_by, 1) > 0
  AND (state = 'running' OR attempted_at > now() - interval '1 hour')
GROUP BY 1
ORDER BY 1`,
		leader: `SELECT leader_id FROM river_leader WHERE expires_at > now() LIMIT 1`,
		queueStatuses: `
SELECT q.name, q.updated_at, q.paused_at IS NOT NULL,
       count(j.id) FILTER (WHERE j.state IN ('available', 'retryable', 'scheduled')),
       count(j.id) FILTER (WHERE j.state = 'running')
FROM river_queue q
LEFT JOIN river_job j ON j.queue = q.name AND j.state IN ('available', 'retryable', 'scheduled', 'running')
GROUP BY q.name, q.updated_at, q.paused_at
ORDER BY q.name`,
		jobs: `
SELECT id, kind, state::text, queue, attempt, max_attempts, created_at, scheduled_at, attempted_at, finalized_at,
       attempted_by[array_length(attempted_by, 1)], args, coalesce(to_jsonb(errors), '[]'::jsonb),
       coalesce(metadata->'output', 'null'::jsonb)
FROM river_job
WHERE ($1 = '' OR kind = $1) AND ($2 = '' OR state::text = $2)
ORDER BY id DESC
LIMIT $3`,
	}
}
