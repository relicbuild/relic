package backgroundjobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"uuid"

	"relic/internal/platform/database"
	"relic/internal/platform/objectstore"
	"relic/internal/platform/telemetry"
	"relic/internal/scanning/cvecatalog"
	"relic/internal/scanning/endoflife"
	"relic/internal/scanning/malicious"
	"relic/internal/scanning/scan"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

const (
	scanTickInterval = time.Minute
	scanClaimBatch   = 200
	scanDBInterval   = 12 * time.Hour
	rematchBatch     = 500
	graphRetention   = 24 * time.Hour
)

// ScanQueue runs scanner jobs apart from light jobs, with its own limit.
const ScanQueue = "scan"

func uniqueActive() river.UniqueOpts {
	return river.UniqueOpts{
		ByArgs: true,
		ByState: []rivertype.JobState{
			rivertype.JobStateAvailable,
			rivertype.JobStatePending,
			rivertype.JobStateRunning,
			rivertype.JobStateRetryable,
			rivertype.JobStateScheduled,
		},
	}
}

// ScanTickArgs finds unscanned content and queues it.
type ScanTickArgs struct{}

// Kind implements river.JobArgs.
func (ScanTickArgs) Kind() string { return "scan_tick" }

// ScanArtifactArgs catalogs and matches one content digest.
type ScanArtifactArgs struct {
	SHA256   string `json:"sha256"`
	FileName string `json:"file_name"`
}

// Kind implements river.JobArgs.
func (ScanArtifactArgs) Kind() string { return "scan_artifact" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (ScanArtifactArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       ScanQueue,
		UniqueOpts:  uniqueActive(),
		MaxAttempts: 3,
	}
}

// ScanDBArgs refreshes the vulnerability database and queues re-matching.
type ScanDBArgs struct {
	Batch int `json:"batch,omitempty"`
}

// Kind implements river.JobArgs.
func (ScanDBArgs) Kind() string { return "scan_db" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (ScanDBArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       ScanQueue,
		UniqueOpts:  uniqueActive(),
		MaxAttempts: 3,
	}
}

// RematchArgs re-matches one stored SBOM.
type RematchArgs struct {
	SHA256 string `json:"sha256"`
}

// Kind implements river.JobArgs.
func (RematchArgs) Kind() string { return "scan_rematch" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (RematchArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       ScanQueue,
		UniqueOpts:  uniqueActive(),
		MaxAttempts: 3,
	}
}

// GraphScanArgs matches a posted component graph (dependency audits).
type GraphScanArgs struct {
	ID uuid.UUID `json:"id"`
}

// Kind implements river.JobArgs.
func (GraphScanArgs) Kind() string { return "scan_graph" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (GraphScanArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       GraphQueue,
		MaxAttempts: 3,
	}
}

// GraphQueue keeps interactive graph scans ahead of background scanning.
const GraphQueue = "scan_graph"

type scanTickWorker struct {
	river.WorkerDefaults[ScanTickArgs]
	scans *scan.Service
	db    *database.DB
}

func (w *scanTickWorker) Work(ctx context.Context, _ *river.Job[ScanTickArgs]) error {
	client := database.JobInserterFromContext(ctx)

	rows, err := w.scans.Claim(ctx, scanClaimBatch)
	if err != nil {
		return err
	}

	if err := queueScans(ctx, w.db, client, rows); err != nil {
		return err
	}

	return w.db.DeleteOldGraphScans(ctx, time.Now().Add(-graphRetention))
}

const scanWaitingReason = "waiting to be queued"

func queueScans(
	ctx context.Context,
	db *database.DB,
	client database.JobInserter,
	rows []database.ClaimUnscannedRow,
) error {
	for i, r := range rows {
		if _, err := client.Insert(ctx, ScanArtifactArgs{
			SHA256:   r.Sha256,
			FileName: r.FileName,
		}, nil); err != nil {
			return errors.Join(fmt.Errorf("queue scan %s: %w", r.Sha256, err), requeueClaims(ctx, db, rows[i:]))
		}
	}

	return nil
}

func requeueClaims(ctx context.Context, db *database.DB, rows []database.ClaimUnscannedRow) error {
	var errs []error

	for _, r := range rows {
		if err := db.FinishScanArtifact(ctx, database.FinishScanArtifactParams{
			Sha256: r.Sha256,
			Status: scan.StatusQueued,
			Reason: scanWaitingReason,
			Now:    time.Now(),
		}); err != nil {
			errs = append(errs, fmt.Errorf("requeue scan %s: %w", r.Sha256, err))
		}
	}

	return errors.Join(errs...)
}

const dbWait = 30 * time.Second

func waitForDB(hasDB func() bool) error {
	if !hasDB() {
		return river.JobSnooze(dbWait)
	}

	return nil
}

type scanArtifactWorker struct {
	river.WorkerDefaults[ScanArtifactArgs]
	scans *scan.Service
	hasDB func() bool
}

func (w *scanArtifactWorker) Work(ctx context.Context, job *river.Job[ScanArtifactArgs]) error {
	if err := waitForDB(w.hasDB); err != nil {
		return err
	}

	return w.scans.ScanArtifact(ctx, job.Args.SHA256, job.Args.FileName)
}

// Timeout implements river.Worker.
func (w *scanArtifactWorker) Timeout(*river.Job[ScanArtifactArgs]) time.Duration {
	return 30 * time.Minute
}

type scanDBWorker struct {
	river.WorkerDefaults[ScanDBArgs]
	grype scan.Grype
	goDB  *scan.GoVulnerabilityDatabase
	db    *database.DB
	log   *slog.Logger
}

func (w *scanDBWorker) Work(ctx context.Context, job *river.Job[ScanDBArgs]) error {
	var goErr error

	if w.goDB != nil {
		if _, err := w.goDB.Update(ctx); err != nil {
			goErr = fmt.Errorf("update the Go vulnerability database: %w", err)
		} else if _, err := database.JobInserterFromContext(ctx).Insert(ctx, ReachabilityArgs{}, nil); err != nil {
			goErr = fmt.Errorf("queue the reachability check: %w", err)
		}
	}

	if err := w.grype.UpdateDB(ctx); err != nil {
		return errors.Join(goErr, fmt.Errorf("update vulnerability database: %w", err))
	}

	if !w.grype.HasDB() {
		return goErr
	}

	return errors.Join(goErr, rematchStale(ctx, w.grype, w.db, w.log, job.Args.Batch))
}

func rematchStale(ctx context.Context, grype scan.Grype, db *database.DB, log *slog.Logger, batch int) error {
	built, err := grype.DBBuilt(ctx)
	if err != nil {
		return err
	}

	client := database.JobInserterFromContext(ctx)

	stale, err := db.ListStaleScans(ctx, database.ListStaleScansParams{
		DbBuilt: &built,
		MaxRows: rematchBatch,
	})
	if err != nil {
		return err
	}

	for _, sha := range stale {
		if _, err := client.Insert(ctx, RematchArgs{
			SHA256: sha,
		}, nil); err != nil {
			return err
		}
	}

	if len(stale) == rematchBatch {
		_, err := client.Insert(ctx, ScanDBArgs{
			Batch: batch + 1,
		}, &river.InsertOpts{
			ScheduledAt: time.Now().Add(time.Minute),
		})
		if err != nil && !errors.Is(err, river.ErrJobCancelledRemotely) {
			return err
		}
	}

	telemetry.ScannerDBBuilt(built, time.Now())
	log.InfoContext(ctx, "vulnerability database current", "built", built, "rematching", len(stale))

	return nil
}

func (w *scanDBWorker) Timeout(*river.Job[ScanDBArgs]) time.Duration { return 30 * time.Minute }

// ScanDBImportArgs installs a vulnerability database uploaded to the
// server (staged in object storage under Key) and queues re-matching.
type ScanDBImportArgs struct {
	Key string `json:"key"`
}

// Kind implements river.JobArgs.
func (ScanDBImportArgs) Kind() string { return "scan_db_import" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (ScanDBImportArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       ScanQueue,
		MaxAttempts: 3,
	}
}

type scanDBImportWorker struct {
	river.WorkerDefaults[ScanDBImportArgs]
	grype   scan.Grype
	imports *scan.DatabaseImports
	db      *database.DB
	log     *slog.Logger
}

func (w *scanDBImportWorker) Work(ctx context.Context, job *river.Job[ScanDBImportArgs]) error {
	ctx, span := telemetry.Tracer().Start(ctx, "scan.ImportDatabase")

	err := w.imports.Install(ctx, w.grype, job.Args.Key)
	if errors.Is(err, objectstore.ErrNotFound) && job.Attempt > 1 {
		err = nil
	}

	if err == nil {
		w.log.InfoContext(ctx, "vulnerability database imported")
		err = rematchStale(ctx, w.grype, w.db, w.log, 0)
	} else if job.Attempt >= job.MaxAttempts {
		if derr := w.imports.Discard(context.WithoutCancel(ctx), job.Args.Key); derr != nil {
			w.log.WarnContext(ctx, "remove the archive of a failed vulnerability database import",
				"key", job.Args.Key, "error", derr)
		}
	}

	telemetry.End(span, err)

	return err
}

func (w *scanDBImportWorker) Timeout(*river.Job[ScanDBImportArgs]) time.Duration {
	return 30 * time.Minute
}

type rematchWorker struct {
	river.WorkerDefaults[RematchArgs]
	scans *scan.Service
	hasDB func() bool
}

func (w *rematchWorker) Work(ctx context.Context, job *river.Job[RematchArgs]) error {
	if err := waitForDB(w.hasDB); err != nil {
		return err
	}

	return w.scans.Rematch(ctx, job.Args.SHA256)
}

type graphScanWorker struct {
	river.WorkerDefaults[GraphScanArgs]
	scans *scan.Service
	hasDB func() bool
}

func (w *graphScanWorker) Work(ctx context.Context, job *river.Job[GraphScanArgs]) error {
	if err := waitForDB(w.hasDB); err != nil {
		return err
	}

	return w.scans.RunGraph(ctx, job.Args.ID, job.Attempt >= job.MaxAttempts)
}

// Scanning configures the scan jobs; nil disables them.
type Scanning struct {
	Service *scan.Service
	Grype   scan.Grype
	// Imports holds vulnerability databases uploaded for the worker.
	Imports *scan.DatabaseImports
	// Malicious reads the malicious-package feeds; nil with them off.
	Malicious *malicious.Refresher
	// EndOfLife reads the end-of-life data.
	EndOfLife *endoflife.Refresher
	// GoVulnerabilities keeps the Go vulnerability database govulncheck
	// reads; nil without govulncheck.
	GoVulnerabilities *scan.GoVulnerabilityDatabase
	// CVEs refreshes CVE facts (EPSS, KEV, records) for findings; nil
	// skips it.
	CVEs *cvecatalog.Refresher
}

func addScanWorkers(workers *river.Workers, db *database.DB, sc *Scanning, log *slog.Logger) {
	river.AddWorker(workers, &scanTickWorker{
		scans: sc.Service,
		db:    db,
	})
	river.AddWorker(workers, &scanArtifactWorker{
		scans: sc.Service,
		hasDB: sc.Grype.HasDB,
	})
	river.AddWorker(workers, &scanDBWorker{
		grype: sc.Grype,
		goDB:  sc.GoVulnerabilities,
		db:    db,
		log:   log,
	})
	river.AddWorker(workers, &scanDBImportWorker{
		grype:   sc.Grype,
		imports: sc.Imports,
		db:      db,
		log:     log,
	})
	river.AddWorker(workers, &rematchWorker{
		scans: sc.Service,
		hasDB: sc.Grype.HasDB,
	})
	river.AddWorker(workers, &graphScanWorker{
		scans: sc.Service,
		hasDB: sc.Grype.HasDB,
	})

	if sc.CVEs != nil {
		river.AddWorker(workers, &cveWorker{
			cves: sc.CVEs,
		})
	}

	river.AddWorker(workers, &maliciousWorker{
		feed:  sc.Malicious,
		scans: sc.Service,
		log:   log,
	})
	river.AddWorker(workers, &operationalRiskWorker{
		data:  sc.EndOfLife,
		scans: sc.Service,
		log:   log,
	})
	river.AddWorker(workers, &reachabilityWorker{
		goDB:  sc.GoVulnerabilities,
		scans: sc.Service,
		log:   log,
	})
}

// ReachabilityArgs analyzes again, from their stored extracts, the Go
// executables of scans analyzed before the Go vulnerability database last
// changed, a batch per run.
type ReachabilityArgs struct {
	// Batch numbers the runs that continue a check.
	Batch int `json:"batch,omitempty"`
}

// Kind implements river.JobArgs.
func (ReachabilityArgs) Kind() string { return "scan_reachability" }

// InsertOpts implements river.JobArgsWithInsertOpts: one run at a time.
func (ReachabilityArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       ScanQueue,
		UniqueOpts:  uniqueActive(),
		MaxAttempts: 3,
	}
}

type reachabilityWorker struct {
	river.WorkerDefaults[ReachabilityArgs]
	goDB  *scan.GoVulnerabilityDatabase
	scans *scan.Service
	log   *slog.Logger
}

func (w *reachabilityWorker) Timeout(*river.Job[ReachabilityArgs]) time.Duration {
	return 30 * time.Minute
}

func (w *reachabilityWorker) Work(ctx context.Context, job *river.Job[ReachabilityArgs]) error {
	if w.goDB == nil {
		return nil
	}

	n, err := w.scans.CheckReachability(ctx, w.goDB.Modified())
	if n == 0 {
		return err
	}

	if err != nil {
		w.log.WarnContext(ctx, "reachability: scans not analyzed again", "error", err)
	}

	w.log.InfoContext(ctx, "reachability: scans analyzed again", "count", n)

	_, err = database.JobInserterFromContext(ctx).Insert(ctx, ReachabilityArgs{
		Batch: job.Args.Batch + 1,
	}, &river.InsertOpts{
		ScheduledAt: time.Now().Add(time.Second),
	})

	return err
}

// OperationalRiskArgs reads the end-of-life data (when Refresh) and assesses
// again the stored scans checked before it last changed, a batch per run.
type OperationalRiskArgs struct {
	Refresh bool `json:"refresh,omitempty"`
	// Batch numbers the runs that continue a check.
	Batch int `json:"batch,omitempty"`
}

// Kind implements river.JobArgs.
func (OperationalRiskArgs) Kind() string { return "scan_operational_risk" }

// InsertOpts implements river.JobArgsWithInsertOpts: one run at a time.
func (OperationalRiskArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       ScanQueue,
		UniqueOpts:  uniqueActive(),
		MaxAttempts: 3,
	}
}

type operationalRiskWorker struct {
	river.WorkerDefaults[OperationalRiskArgs]
	data  *endoflife.Refresher
	scans *scan.Service
	log   *slog.Logger
}

func (w *operationalRiskWorker) Timeout(*river.Job[OperationalRiskArgs]) time.Duration {
	return 30 * time.Minute
}

func (w *operationalRiskWorker) Work(ctx context.Context, job *river.Job[OperationalRiskArgs]) error {
	var refreshErr error

	if job.Args.Refresh && w.data != nil {
		_, refreshErr = w.data.Refresh(ctx)
	}

	n, err := w.scans.CheckOperationalRisks(ctx)
	if err != nil {
		return errors.Join(refreshErr, err)
	}

	if n > 0 {
		w.log.InfoContext(ctx, "operational risk: scans assessed", "count", n)

		if _, err := database.JobInserterFromContext(ctx).Insert(ctx, OperationalRiskArgs{
			Batch: job.Args.Batch + 1,
		}, &river.InsertOpts{
			ScheduledAt: time.Now().Add(time.Second),
		}); err != nil {
			return errors.Join(refreshErr, err)
		}
	}

	return refreshErr
}

// MaliciousArgs reads the malicious-package feeds (when Refresh) and
// checks stored scans against advisories that changed since they were
// checked, a batch per run.
type MaliciousArgs struct {
	Refresh bool `json:"refresh,omitempty"`
	// Batch numbers the runs that continue a check.
	Batch int `json:"batch,omitempty"`
}

// Kind implements river.JobArgs.
func (MaliciousArgs) Kind() string { return "scan_malicious" }

// InsertOpts implements river.JobArgsWithInsertOpts: one run at a time.
func (MaliciousArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       ScanQueue,
		UniqueOpts:  uniqueActive(),
		MaxAttempts: 3,
	}
}

type maliciousWorker struct {
	river.WorkerDefaults[MaliciousArgs]
	feed  *malicious.Refresher
	scans *scan.Service
	log   *slog.Logger
}

func (w *maliciousWorker) Timeout(*river.Job[MaliciousArgs]) time.Duration { return 30 * time.Minute }

func (w *maliciousWorker) Work(ctx context.Context, job *river.Job[MaliciousArgs]) error {
	var feedErr error

	if job.Args.Refresh && w.feed != nil {
		_, feedErr = w.feed.Refresh(ctx)
	}

	n, err := w.scans.CheckMalicious(ctx)
	if err != nil {
		return errors.Join(feedErr, err)
	}

	if n > 0 {
		w.log.InfoContext(ctx, "malicious packages: scans checked", "count", n)

		if _, err := database.JobInserterFromContext(ctx).Insert(ctx, MaliciousArgs{
			Batch: job.Args.Batch + 1,
		}, &river.InsertOpts{
			ScheduledAt: time.Now().Add(time.Second),
		}); err != nil {
			return errors.Join(feedErr, err)
		}
	}

	return feedErr
}

// CVERefreshArgs updates CVE facts from the feeds that are due.
type CVERefreshArgs struct{}

// Kind implements river.JobArgs.
func (CVERefreshArgs) Kind() string { return "cve_refresh" }

// InsertOpts implements river.JobArgsWithInsertOpts: one run at a time.
func (CVERefreshArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		UniqueOpts:  uniqueActive(),
		MaxAttempts: 3,
	}
}

type cveWorker struct {
	river.WorkerDefaults[CVERefreshArgs]
	cves *cvecatalog.Refresher
}

func (w *cveWorker) Timeout(*river.Job[CVERefreshArgs]) time.Duration { return 30 * time.Minute }

func (w *cveWorker) Work(ctx context.Context, _ *river.Job[CVERefreshArgs]) error {
	return w.cves.Refresh(ctx)
}

func scanPeriodicJobs() []*river.PeriodicJob {
	return []*river.PeriodicJob{
		river.NewPeriodicJob(
			river.PeriodicInterval(6*time.Hour),
			func() (river.JobArgs, *river.InsertOpts) {
				return MaliciousArgs{
					Refresh: true,
				}, nil
			},
			&river.PeriodicJobOpts{
				RunOnStart: true,
			},
		),
		river.NewPeriodicJob(
			river.PeriodicInterval(24*time.Hour),
			func() (river.JobArgs, *river.InsertOpts) {
				return OperationalRiskArgs{
					Refresh: true,
				}, nil
			},
			&river.PeriodicJobOpts{
				RunOnStart: true,
			},
		),
		river.NewPeriodicJob(
			river.PeriodicInterval(scanTickInterval),
			func() (river.JobArgs, *river.InsertOpts) {
				return ScanTickArgs{}, nil
			},
			nil,
		),
		river.NewPeriodicJob(
			river.PeriodicInterval(scanDBInterval),
			func() (river.JobArgs, *river.InsertOpts) {
				return ScanDBArgs{}, nil
			},
			&river.PeriodicJobOpts{
				RunOnStart: true,
			},
		),
		river.NewPeriodicJob(
			river.PeriodicInterval(time.Hour),
			func() (river.JobArgs, *river.InsertOpts) {
				return CVERefreshArgs{}, nil
			},
			&river.PeriodicJobOpts{
				RunOnStart: true,
			},
		),
	}
}
