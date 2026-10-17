package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

type jobQueries struct {
	stats, queues, lastErrors, workers, leader, queueStatuses, jobs string
}

// JobStat is the number of background jobs of one kind in one state.
type JobStat struct {
	Kind         string
	State        string
	Count        int64
	LastFinished *time.Time
}

// JobStats counts River jobs by kind and state.
func (db *DB) JobStats(ctx context.Context) ([]JobStat, error) {
	return collect(ctx, db, db.engine.jobQueries().stats, nil, func(s *JobStat) []any {
		return []any{
			&s.Kind,
			&s.State,
			&s.Count,
			&s.LastFinished,
		}
	})
}

// JobQueue is one job kind's waiting work.
type JobQueue struct {
	Kind              string
	Available         int64
	Retryable         int64
	OldestAvailableAt *time.Time
}

// JobQueues counts River jobs that wait: available now (with the oldest
// one's scheduled time) and retryable.
func (db *DB) JobQueues(ctx context.Context) ([]JobQueue, error) {
	return collect(ctx, db, db.engine.jobQueries().queues, nil, func(q *JobQueue) []any {
		return []any{
			&q.Kind,
			&q.Available,
			&q.Retryable,
			&q.OldestAvailableAt,
		}
	})
}

// JobError is the most recent error recorded for a job kind.
type JobError struct {
	Kind  string
	Error string
	At    time.Time
}

// LastJobErrors returns the latest error of each job kind that has one.
func (db *DB) LastJobErrors(ctx context.Context) ([]JobError, error) {
	return collect(ctx, db, db.engine.jobQueries().lastErrors, nil, func(e *JobError) []any {
		return []any{
			&e.Kind,
			&e.Error,
			&e.At,
		}
	})
}

// JobWorker is a queue client seen running jobs lately.
type JobWorker struct {
	ID         string
	Running    int64
	LastActive time.Time
}

// JobWorkers returns the queue clients that started a job in the last hour.
func (db *DB) JobWorkers(ctx context.Context) ([]JobWorker, error) {
	return collect(ctx, db, db.engine.jobQueries().workers, nil, func(w *JobWorker) []any {
		return []any{
			&w.ID,
			&w.Running,
			&w.LastActive,
		}
	})
}

// JobLeader returns the ID of the queue client elected leader, or "" when
// none holds the lease.
func (db *DB) JobLeader(ctx context.Context) (string, error) {
	ids, err := collect(ctx, db, db.engine.jobQueries().leader, nil, func(id *string) []any {
		return []any{
			id,
		}
	})
	if err != nil {
		return "", fmt.Errorf("job leader: %w", err)
	}

	if len(ids) == 0 {
		return "", nil
	}

	return ids[0], nil
}

// JobQueueStatus is a named queue with its waiting and running jobs.
type JobQueueStatus struct {
	Name    string
	Updated time.Time
	Paused  bool
	Waiting int64
	Running int64
}

// JobQueueStatuses returns the queues producers report on, with their jobs.
func (db *DB) JobQueueStatuses(ctx context.Context) ([]JobQueueStatus, error) {
	return collect(ctx, db, db.engine.jobQueries().queueStatuses, nil, func(q *JobQueueStatus) []any {
		return []any{
			&q.Name,
			&q.Updated,
			&q.Paused,
			&q.Waiting,
			&q.Running,
		}
	})
}

// Job is one River job with its errors.
type Job struct {
	ID          int64
	Kind        string
	State       string
	Queue       string
	Attempt     int16
	MaxAttempts int16
	Created     time.Time
	Scheduled   time.Time
	Attempted   *time.Time
	Finalized   *time.Time
	Worker      *string
	Args        []byte
	Errors      []JobAttemptError
	// Output is what the job recorded (river.RecordOutput), JSON, or null.
	Output []byte
}

// JobAttemptError is one failed attempt of a job.
type JobAttemptError struct {
	Attempt int       `json:"attempt"`
	At      time.Time `json:"at"`
	Error   string    `json:"error"`
}

// Jobs returns the newest River jobs, of one kind and state when they are
// set, with their errors.
func (db *DB) Jobs(ctx context.Context, kind, state string, limit int32) ([]Job, error) {
	type row struct {
		Job

		errs []byte
	}

	rows, err := collect(ctx, db, db.engine.jobQueries().jobs, []any{
		kind,
		state,
		limit,
	}, func(r *row) []any {
		return []any{
			&r.ID,
			&r.Kind,
			&r.State,
			&r.Queue,
			&r.Attempt,
			&r.MaxAttempts,
			&r.Created,
			&r.Scheduled,
			&r.Attempted,
			&r.Finalized,
			&r.Worker,
			&r.Args,
			&r.errs,
			&r.Output,
		}
	})
	if err != nil {
		return nil, fmt.Errorf("jobs: %w", err)
	}

	out := make([]Job, 0, len(rows))
	for _, r := range rows {
		if err := json.Unmarshal(r.errs, &r.Errors); err != nil {
			return nil, fmt.Errorf("job %d errors: %w", r.ID, err)
		}

		out = append(out, r.Job)
	}

	return out, nil
}

func collect[T any](ctx context.Context, db *DB, query string, args []any, targets func(*T) []any) ([]T, error) {
	rows, err := db.engine.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []T

	for rows.Next() {
		var v T
		if err := rows.Scan(targets(&v)...); err != nil {
			return nil, err
		}

		out = append(out, v)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return out, nil
}

// JobInserter enqueues background jobs.
type JobInserter interface {
	Insert(ctx context.Context, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error)
}

// JobClient is a River client on this database: it enqueues
// jobs and, with workers configured, runs them.
type JobClient interface {
	JobInserter
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// NewJobClient returns a River client on this database's driver.
func (db *DB) NewJobClient(cfg *river.Config) (JobClient, error) {
	return db.engine.newJobClient(cfg)
}

// JobInserterFromContext returns the client working the current job.
func JobInserterFromContext(ctx context.Context) JobInserter {
	if client, err := river.ClientFromContextSafely[pgx.Tx](ctx); err == nil {
		return client
	}

	return river.ClientFromContext[*sql.Tx](ctx)
}
