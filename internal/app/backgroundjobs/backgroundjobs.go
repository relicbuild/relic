// Package backgroundjobs defines Relic's background jobs on the River queue.
package backgroundjobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"relic/internal/access/audit"
	"relic/internal/app/config"
	"relic/internal/artifacts/cleanup"
	"relic/internal/artifacts/formats/docker"
	"relic/internal/artifacts/repository"
	"relic/internal/artifacts/usage"
	"relic/internal/delivery/replication"
	"relic/internal/delivery/webhook"
	"relic/internal/platform/database"
	"relic/internal/platform/telemetry"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

const (
	gcBatch             = 500
	replicationInterval = time.Minute
)

// GCArgs collects unreferenced blobs.
type GCArgs struct{}

// Kind implements river.JobArgs.
func (GCArgs) Kind() string { return "gc" }

// TrashPurgeArgs deletes trash can entries past the retention period.
type TrashPurgeArgs struct{}

// Kind implements river.JobArgs.
func (TrashPurgeArgs) Kind() string { return "trash_purge" }

const trashPurgeInterval = time.Hour

type trashWorker struct {
	river.WorkerDefaults[TrashPurgeArgs]
	repos     *repository.Service
	retention time.Duration
	log       *slog.Logger
}

func (w *trashWorker) Work(ctx context.Context, _ *river.Job[TrashPurgeArgs]) error {
	n, err := w.repos.PurgeTrash(ctx, time.Now().Add(-w.retention))
	telemetry.TrashPurged(n)

	if n > 0 {
		w.log.InfoContext(ctx, "purged trash entries", "count", n)
	}

	return err
}

// UsageSnapshotArgs records every repository's files and bytes for the day.
type UsageSnapshotArgs struct{}

// Kind implements river.JobArgs.
func (UsageSnapshotArgs) Kind() string { return "usage_snapshot" }

const usageSnapshotInterval = 6 * time.Hour

type usageSnapshotWorker struct {
	river.WorkerDefaults[UsageSnapshotArgs]
	usage *usage.Service
}

func (w *usageSnapshotWorker) Work(ctx context.Context, _ *river.Job[UsageSnapshotArgs]) error {
	return w.usage.Snapshot(ctx)
}

// WebhookDispatchArgs sends the webhook deliveries that are due.
type WebhookDispatchArgs struct{}

// Kind implements river.JobArgs.
func (WebhookDispatchArgs) Kind() string { return "webhook_dispatch" }

// InsertOpts keeps at most one dispatch queued or running.
func (WebhookDispatchArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		UniqueOpts: river.UniqueOpts{
			ByState: []rivertype.JobState{
				rivertype.JobStateAvailable,
				rivertype.JobStatePending,
				rivertype.JobStateRunning,
				rivertype.JobStateRetryable,
				rivertype.JobStateScheduled,
			},
		},
		MaxAttempts: 1,
	}
}

const (
	webhookInterval = 15 * time.Second
	webhookRounds   = 20
)

type webhookWorker struct {
	river.WorkerDefaults[WebhookDispatchArgs]
	webhooks *webhook.Service
}

func (w *webhookWorker) Work(ctx context.Context, _ *river.Job[WebhookDispatchArgs]) error {
	for range webhookRounds {
		n, err := w.webhooks.Dispatch(ctx)
		if err != nil || n == 0 {
			return err
		}
	}

	return nil
}

// AuditPurgeArgs deletes audit events past the retention period.
type AuditPurgeArgs struct{}

// Kind implements river.JobArgs.
func (AuditPurgeArgs) Kind() string { return "audit_purge" }

const (
	auditPurgeInterval = 24 * time.Hour
	auditRetention     = 365 * 24 * time.Hour
)

type auditPurgeWorker struct {
	river.WorkerDefaults[AuditPurgeArgs]
	audit     *audit.Service
	retention time.Duration
	log       *slog.Logger
}

func (w *auditPurgeWorker) Work(ctx context.Context, _ *river.Job[AuditPurgeArgs]) error {
	n, err := w.audit.Purge(ctx, w.retention)
	if n > 0 {
		w.log.InfoContext(ctx, "purged audit events", "count", n)
	}

	return err
}

// ReplicationTickArgs enqueues the replications whose schedule fired.
type ReplicationTickArgs struct{}

// Kind implements river.JobArgs.
func (ReplicationTickArgs) Kind() string { return "replication_tick" }

// ReplicationArgs runs one repository's push replication.
type ReplicationArgs struct {
	// Repository keeps its JSON name so jobs queued by an earlier version
	// still run.
	Repository string `json:"repo_key"`
}

// Kind implements river.JobArgs.
func (ReplicationArgs) Kind() string { return "replication" }

// InsertOpts makes runs unique per repository while one is queued or
// running.
func (ReplicationArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		UniqueOpts: river.UniqueOpts{
			ByArgs: true,
			ByState: []rivertype.JobState{
				rivertype.JobStateAvailable,
				rivertype.JobStatePending,
				rivertype.JobStateRunning,
				rivertype.JobStateRetryable,
				rivertype.JobStateScheduled,
			},
		},
		MaxAttempts: 3,
	}
}

// CleanupArgs runs every enabled cleanup policy.
type CleanupArgs struct{}

// Kind implements river.JobArgs.
func (CleanupArgs) Kind() string { return "cleanup" }

// InsertOpts makes one cleanup queued or running at a time.
func (CleanupArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		UniqueOpts: river.UniqueOpts{
			ByState: []rivertype.JobState{
				rivertype.JobStateAvailable,
				rivertype.JobStatePending,
				rivertype.JobStateRunning,
				rivertype.JobStateRetryable,
				rivertype.JobStateScheduled,
			},
		},
		MaxAttempts: 1,
	}
}

const cleanupInterval = 24 * time.Hour

type cleanupWorker struct {
	river.WorkerDefaults[CleanupArgs]
	cleanup *cleanup.Service
}

func (w *cleanupWorker) Work(ctx context.Context, _ *river.Job[CleanupArgs]) error {
	_, err := w.cleanup.RunAll(ctx)

	return err
}

// StorageCheckArgs compares the object store with the blob rows
// (repository.Service.CheckStorage).
type StorageCheckArgs struct{}

// Kind implements river.JobArgs.
func (StorageCheckArgs) Kind() string { return "storage_check" }

// InsertOpts makes one check queued or running at a time.
func (StorageCheckArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		UniqueOpts: river.UniqueOpts{
			ByState: []rivertype.JobState{
				rivertype.JobStateAvailable,
				rivertype.JobStatePending,
				rivertype.JobStateRunning,
				rivertype.JobStateRetryable,
				rivertype.JobStateScheduled,
			},
		},
		MaxAttempts: 1,
	}
}

const (
	storageCheckInterval    = 24 * time.Hour
	storageCheckVerifyBytes = 10 << 30
)

type storageCheckWorker struct {
	river.WorkerDefaults[StorageCheckArgs]
	repos *repository.Service
	grace time.Duration
	log   *slog.Logger
}

func (w *storageCheckWorker) Work(ctx context.Context, _ *river.Job[StorageCheckArgs]) error {
	report, err := w.repos.CheckStorage(ctx, repository.StorageCheck{
		Grace:       w.grace,
		VerifyBytes: storageCheckVerifyBytes,
	})

	w.log.InfoContext(ctx, "storage check", "objects", report.Objects, "adopted", report.Adopted,
		"missing", report.Missing, "wrong_size", report.WrongSize, "corrupt", report.Corrupt,
		"verified", report.Verified)

	if outErr := river.RecordOutput(ctx, report); outErr != nil {
		err = errors.Join(err, outErr)
	}

	return err
}

// Timeout implements river.Worker.
func (w *storageCheckWorker) Timeout(*river.Job[StorageCheckArgs]) time.Duration {
	return 12 * time.Hour
}

type gcWorker struct {
	river.WorkerDefaults[GCArgs]
	db    *database.DB
	repos *repository.Service
	grace time.Duration
	log   *slog.Logger
}

func (w *gcWorker) Work(ctx context.Context, _ *river.Job[GCArgs]) error {
	expired, expireErr := docker.ExpireUploads(ctx, w.repos, docker.UploadTTL)
	if expired > 0 {
		w.log.InfoContext(ctx, "deleted pushed blobs no manifest references", "count", expired)
	}

	before, statErr := w.db.BlobStats(ctx)
	n, err := w.repos.CollectGarbage(ctx, w.grace, gcBatch)
	err = errors.Join(err, expireErr)

	swept, sweepErr := w.repos.SweepTemporaryUploads(ctx)
	telemetry.GCTemporaryFilesDeleted(swept)

	if swept > 0 {
		w.log.InfoContext(ctx, "deleted expired temporary uploads", "count", swept)
	}

	err = errors.Join(err, sweepErr)

	var reclaimed int64

	after, afterErr := w.db.BlobStats(ctx)
	if statsErr := errors.Join(statErr, afterErr); statsErr != nil {
		w.log.WarnContext(ctx, "read blob stats around garbage collection", "error", statsErr)
	} else {
		reclaimed = before.UnreferencedBytes - after.UnreferencedBytes
	}

	telemetry.GCRun(err, n, reclaimed)

	if n > 0 {
		w.log.InfoContext(ctx, "collected blobs", "count", n)
	}

	return err
}

type tickWorker struct {
	river.WorkerDefaults[ReplicationTickArgs]
	db  *database.DB
	log *slog.Logger

	purgedUnixNano atomic.Int64
}

const (
	tombstoneRetention     = 90 * 24 * time.Hour
	tombstonePurgeInterval = time.Hour
)

func (w *tickWorker) Work(ctx context.Context, _ *river.Job[ReplicationTickArgs]) error {
	purged := w.purgedUnixNano.Load()
	if time.Since(time.Unix(0, purged)) > tombstonePurgeInterval &&
		w.purgedUnixNano.CompareAndSwap(purged, time.Now().UnixNano()) {
		if _, err := w.db.PurgeTombstones(ctx, time.Now().Add(-tombstoneRetention)); err != nil {
			w.log.WarnContext(ctx, "purge replication tombstones", "error", err)
		}
	}

	rows, err := w.db.ListReplications(ctx)
	if err != nil {
		return err
	}

	client := database.JobInserterFromContext(ctx)
	now := time.Now()
	configured := make([]string, 0, len(rows))

	for _, r := range rows {
		cfg, _, err := replication.NewPusher(w.db, nil, nil, w.log).Load(ctx, r.Repository)
		if err != nil || (cfg.Enabled && cfg.TargetURL != "") {
			configured = append(configured, r.Repository)
		}

		if err != nil || !cfg.Enabled || cfg.TargetURL == "" || cfg.Schedule == "" {
			continue
		}

		due, err := replication.Due(cfg.Schedule, r.LastRun, now, replicationInterval)
		if err != nil {
			w.log.WarnContext(ctx, "invalid replication schedule", "repo", r.Repository, "error", err)
			continue
		}

		if due {
			if _, err := client.Insert(ctx, ReplicationArgs{
				Repository: r.Repository,
			}, nil); err != nil {
				return fmt.Errorf("enqueue replication %s: %w", r.Repository, err)
			}
		}
	}

	telemetry.ReplicationsConfigured(configured)

	return nil
}

type replicationWorker struct {
	river.WorkerDefaults[ReplicationArgs]
	pusher *replication.Pusher
	log    *slog.Logger
}

func (w *replicationWorker) Work(ctx context.Context, job *river.Job[ReplicationArgs]) error {
	start := time.Now()

	res, err := w.pusher.Run(ctx, job.Args.Repository)
	if errors.Is(err, replication.ErrNotConfigured) {
		return river.JobCancel(err)
	}

	telemetry.ReplicationRun(
		ctx,
		job.Args.Repository,
		err,
		time.Since(start),
		res.Pushed,
		res.Pulled,
		res.Skipped,
		res.Deleted,
	)
	w.log.InfoContext(
		ctx,
		"replication run",
		"repo",
		job.Args.Repository,
		"pushed",
		res.Pushed,
		"pulled",
		res.Pulled,
		"skipped",
		res.Skipped,
		"deleted",
		res.Deleted,
		"error",
		err,
	)

	return err
}

// NewWorkerClient returns a River client that runs every job kind, with
// garbage collection and the replication scheduler as periodic jobs, and
// Relic Scan's jobs unless scanning is nil (scanners not installed).
func NewWorkerClient(
	db *database.DB,
	repos *repository.Service,
	pusher *replication.Pusher,
	scanning *Scanning,
	webhooks *webhook.Service,
	gc config.GarbageCollection,
	trash config.Trash,
	auditLog config.Audit,
	log *slog.Logger,
) (database.JobClient, error) {
	retention := auditLog.Retention
	if retention <= 0 {
		retention = auditRetention
	}

	workers := river.NewWorkers()
	river.AddWorker(workers, &gcWorker{
		db:    db,
		repos: repos,
		grace: gc.GracePeriod,
		log:   log,
	})
	river.AddWorker(workers, &trashWorker{
		repos:     repos,
		retention: trash.Retention,
		log:       log,
	})
	river.AddWorker(workers, &tickWorker{
		db:  db,
		log: log,
	})
	river.AddWorker(workers, &replicationWorker{
		pusher: pusher,
		log:    log,
	})
	river.AddWorker(workers, &cleanupWorker{
		cleanup: cleanup.New(db, repos, log),
	})
	river.AddWorker(workers, &auditPurgeWorker{
		audit:     audit.New(db, log),
		retention: retention,
		log:       log,
	})
	river.AddWorker(workers, &usageSnapshotWorker{
		usage: usage.NewService(db, repos.Projects),
	})
	river.AddWorker(workers, &storageCheckWorker{
		repos: repos,
		grace: gc.GracePeriod,
		log:   log,
	})

	periodic := []*river.PeriodicJob{
		river.NewPeriodicJob(
			river.PeriodicInterval(gc.Interval),
			func() (river.JobArgs, *river.InsertOpts) {
				return GCArgs{}, nil
			},
			nil,
		),
		river.NewPeriodicJob(
			river.PeriodicInterval(trashPurgeInterval),
			func() (river.JobArgs, *river.InsertOpts) {
				return TrashPurgeArgs{}, nil
			},
			&river.PeriodicJobOpts{
				RunOnStart: true,
			},
		),
		river.NewPeriodicJob(
			river.PeriodicInterval(cleanupInterval),
			func() (river.JobArgs, *river.InsertOpts) {
				return CleanupArgs{}, nil
			},
			nil,
		),
		river.NewPeriodicJob(
			river.PeriodicInterval(auditPurgeInterval),
			func() (river.JobArgs, *river.InsertOpts) {
				return AuditPurgeArgs{}, nil
			},
			nil,
		),
		river.NewPeriodicJob(
			river.PeriodicInterval(usageSnapshotInterval),
			func() (river.JobArgs, *river.InsertOpts) {
				return UsageSnapshotArgs{}, nil
			},
			&river.PeriodicJobOpts{
				RunOnStart: true,
			},
		),
		river.NewPeriodicJob(
			river.PeriodicInterval(storageCheckInterval),
			func() (river.JobArgs, *river.InsertOpts) {
				return StorageCheckArgs{}, nil
			},
			nil,
		),
		river.NewPeriodicJob(
			river.PeriodicInterval(replicationInterval),
			func() (river.JobArgs, *river.InsertOpts) {
				return ReplicationTickArgs{}, nil
			},
			&river.PeriodicJobOpts{
				RunOnStart: true,
			},
		),
	}
	queues := map[string]river.QueueConfig{
		river.QueueDefault: {
			MaxWorkers: 4,
		},
	}

	if webhooks != nil {
		river.AddWorker(workers, &webhookWorker{
			webhooks: webhooks,
		})

		periodic = append(periodic, river.NewPeriodicJob(
			river.PeriodicInterval(webhookInterval),
			func() (river.JobArgs, *river.InsertOpts) {
				return WebhookDispatchArgs{}, nil
			},
			nil,
		))
	}

	if scanning != nil {
		addScanWorkers(workers, db, scanning, log)

		periodic = append(periodic, scanPeriodicJobs()...)
		queues[ScanQueue] = river.QueueConfig{
			MaxWorkers: 2,
		}
		queues[GraphQueue] = river.QueueConfig{
			MaxWorkers: 4,
		}
	}

	return db.NewJobClient(&river.Config{
		Logger:       log,
		Queues:       queues,
		Workers:      workers,
		PeriodicJobs: periodic,
		Middleware:   middleware(Kinds(scanning != nil)),
	})
}

// Kinds are the job kinds the worker runs; with scanning, Relic Scan's too.
func Kinds(scanning bool) []string {
	kinds := []string{
		GCArgs{}.Kind(),
		TrashPurgeArgs{}.Kind(),
		ReplicationTickArgs{}.Kind(),
		ReplicationArgs{}.Kind(),
		StorageCheckArgs{}.Kind(),
		CleanupArgs{}.Kind(),
		AuditPurgeArgs{}.Kind(),
		WebhookDispatchArgs{}.Kind(),
		UsageSnapshotArgs{}.Kind(),
	}
	if scanning {
		kinds = append(
			kinds,
			ScanTickArgs{}.Kind(),
			ScanArtifactArgs{}.Kind(),
			ScanDBArgs{}.Kind(),
			ScanDBImportArgs{}.Kind(),
			RematchArgs{}.Kind(),
			GraphScanArgs{}.Kind(),
			CVERefreshArgs{}.Kind(),
			MaliciousArgs{}.Kind(),
		)
	}

	return kinds
}

// NewInsertClient returns a client that only enqueues (used by the server).
func NewInsertClient(db *database.DB, log *slog.Logger) (database.JobClient, error) {
	return db.NewJobClient(&river.Config{
		Logger:     log,
		Middleware: middleware(nil),
	})
}
