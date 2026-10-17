// Package systemstatus reports on the server itself: stored content and the
// background jobs.
package systemstatus

import (
	"context"
	"fmt"
	"time"

	"relic/internal/platform/database"
)

// Storage is stored content and the part garbage collection will reclaim.
type Storage struct {
	Blobs             int64
	Bytes             int64
	UnreferencedBlobs int64
	UnreferencedBytes int64
}

// JobKind is one kind of background job: its jobs by state, when one last
// finished, and its latest error.
type JobKind struct {
	Kind         string
	States       map[string]int64
	LastFinished *time.Time
	LastError    string
	LastErrorAt  *time.Time
}

// Service reports on the server.
type Service struct {
	db *database.DB
}

// NewService returns a Service reading db.
func NewService(db *database.DB) *Service {
	return &Service{
		db: db,
	}
}

// Storage returns the stored content.
func (s *Service) Storage(ctx context.Context) (Storage, error) {
	b, err := s.db.BlobStats(ctx)
	if err != nil {
		return Storage{}, fmt.Errorf("blob stats: %w", err)
	}

	return Storage(b), nil
}

// Jobs returns the background job kinds, in the order the queue lists them.
func (s *Service) Jobs(ctx context.Context) ([]JobKind, error) {
	stats, err := s.db.JobStats(ctx)
	if err != nil {
		return nil, fmt.Errorf("job stats: %w", err)
	}

	var out []JobKind

	index := map[string]int{}
	for _, st := range stats {
		i, ok := index[st.Kind]
		if !ok {
			i = len(out)
			index[st.Kind] = i
			out = append(out, JobKind{
				Kind:   st.Kind,
				States: map[string]int64{},
			})
		}

		k := &out[i]

		k.States[st.State] = st.Count
		if st.LastFinished != nil && (k.LastFinished == nil || st.LastFinished.After(*k.LastFinished)) {
			k.LastFinished = st.LastFinished
		}
	}

	errs, err := s.db.LastJobErrors(ctx)
	if err != nil {
		return nil, fmt.Errorf("job errors: %w", err)
	}

	for _, e := range errs {
		if i, ok := index[e.Kind]; ok {
			at := e.At
			out[i].LastError, out[i].LastErrorAt = e.Error, &at
		}
	}

	return out, nil
}

// Worker is a process working the job queue.
type Worker struct {
	ID         string
	Running    int64
	LastActive time.Time
	Leader     bool
}

// Workers returns the processes that ran jobs in the last hour, marking the
// elected leader.
func (s *Service) Workers(ctx context.Context) ([]Worker, error) {
	rows, err := s.db.JobWorkers(ctx)
	if err != nil {
		return nil, fmt.Errorf("job workers: %w", err)
	}

	leader, err := s.db.JobLeader(ctx)
	if err != nil {
		return nil, fmt.Errorf("job leader: %w", err)
	}

	out := make([]Worker, 0, len(rows)+1)
	seen := false

	for _, r := range rows {
		w := Worker{
			ID:         r.ID,
			Running:    r.Running,
			LastActive: r.LastActive,
			Leader:     r.ID == leader,
		}
		seen = seen || w.Leader
		out = append(out, w)
	}

	if leader != "" && !seen {
		out = append(out, Worker{
			ID:     leader,
			Leader: true,
		})
	}

	return out, nil
}

// Queue is a named job queue.
type Queue = database.JobQueueStatus

// Queues returns the job queues.
func (s *Service) Queues(ctx context.Context) ([]Queue, error) {
	queues, err := s.db.JobQueueStatuses(ctx)
	if err != nil {
		return nil, fmt.Errorf("job queues: %w", err)
	}

	return queues, nil
}

// Job is one background job with its errors.
type Job = database.Job

const maxJobs = 500

// ListJobs returns the newest jobs, narrowed to a kind and a state when
// they are set; limit is 50 when unset.
func (s *Service) ListJobs(ctx context.Context, kind, state string, limit int32) ([]Job, error) {
	switch {
	case limit <= 0:
		limit = 50
	case limit > maxJobs:
		limit = maxJobs
	}

	jobs, err := s.db.Jobs(ctx, kind, state, limit)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}

	return jobs, nil
}
