package telemetry

import (
	"context"
	"slices"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

var attrStage = attribute.Key("relic.scan.stage")

var jobBuckets = []float64{
	.05,
	.1,
	.5,
	1,
	5,
	15,
	60,
	300,
	900,
	1800,
	3600,
}

var (
	// PromQL: sum by (kind, result) (rate(relic_jobs_total[5m]))
	jobsTotal = counterVec(
		"jobs_total",
		"Background jobs worked, by kind and result (success, error, cancelled, snoozed).",
		"kind",
		"result",
	)

	// PromQL: histogram_quantile(0.95, sum by (le, kind) (rate(relic_job_duration_seconds_bucket[15m])))
	jobDuration = histogramVec("job_duration_seconds", "Background job duration by kind.", jobBuckets, "kind")

	// PromQL: sum by (kind) (relic_jobs_queued)
	jobsQueued = gaugeVec("jobs_queued", "Jobs available to run now, by kind.", "kind")

	// PromQL: sum by (kind) (relic_jobs_retryable)
	jobsRetryable = gaugeVec("jobs_retryable", "Jobs waiting to be retried after an error, by kind.", "kind")

	// PromQL: max by (kind) (relic_job_oldest_available_age_seconds) > 1800   (alert RelicJobBacklog)
	jobOldest = gaugeVec(
		"job_oldest_available_age_seconds",
		"Age of the oldest job available to run, by kind (0 when none waits).",
		"kind",
	)

	// PromQL: max(relic_scan_backlog)   (alert RelicScanBacklog: deriv over 1h > 0)
	scanBacklog = gaugeVec("scan_backlog", "Artifacts queued for Relic Scan.")

	// PromQL: sum by (result) (rate(relic_scans_total[1h]))
	scansTotal = counterVec(
		"scans_total",
		"Artifact scans finished, by result (done, partial, failed, skipped, queued, error).",
		"result",
	)

	// PromQL: histogram_quantile(0.95, sum by (le, stage) (rate(relic_scan_duration_seconds_bucket[1h])))
	scanDuration = histogramVec(
		"scan_duration_seconds",
		"Scanner run time by stage: catalog (Syft), match (Grype), secrets (gitleaks), misconfiguration (Trivy), license (ScanCode), model and reachability (govulncheck).",
		jobBuckets,
		"stage",
	)

	// PromQL: sum by (severity) (relic_findings)
	findings = gaugeVec(
		"findings",
		"Vulnerability findings on content stored in repositories, by severity.",
		"severity",
	)

	// PromQL: sum by (reason) (increase(relic_downloads_blocked_total[1h]))
	downloadsBlocked = counterVec(
		"downloads_blocked_total",
		"Downloads refused by a scan policy, by reason (unscanned, quarantine, policy).",
		"reason",
	)

	// PromQL: sum by (result) (increase(relic_gc_runs_total[6h]))   (alert RelicGCFailing: no success in 6h)
	gcRuns = counterVec("gc_runs_total", "Garbage collection runs by result.", "result")

	// PromQL: rate(relic_gc_deleted_blobs_total[1h])
	gcDeletedBlobs = counterVec("gc_deleted_blobs_total", "Blobs removed by garbage collection.")

	// PromQL: rate(relic_gc_deleted_bytes_total[1h])
	gcDeletedBytes = counterVec(
		"gc_deleted_bytes_total",
		"Bytes reclaimed by garbage collection (the drop in unreferenced bytes over a run).",
	)

	// PromQL: increase(relic_gc_deleted_temporary_files_total[1d])
	gcDeletedTemporaryFiles = counterVec(
		"gc_deleted_temporary_files_total",
		"Expired uploads and staging files the filesystem object store's garbage collection deleted.",
	)

	// PromQL: increase(relic_trash_purged_total[1d])
	trashPurged = counterVec("trash_purged_total", "Trash can entries purged after the retention period.")

	// PromQL: sum by (result) (increase(relic_replication_runs_total[1h]))
	replicationRuns = repoCounterVec("replication_runs_total", "Push replication runs by result.", "result")

	// PromQL: histogram_quantile(0.95, sum by (le) (rate(relic_replication_duration_seconds_bucket[1h])))
	replicationDuration = histogramVec("replication_duration_seconds", "Replication run duration.", jobBuckets)

	// PromQL: sum by (result) (rate(relic_replication_events_total[1h]))
	replicationEvents = counterVec(
		"replication_events_total",
		"Files handled by replication, by result (pushed, pulled, skipped, deleted).",
		"result",
	)

	// PromQL: max by (repository) (relic_replication_consecutive_failures) >= 3   (alert RelicReplicationFailing)
	replicationFailures = gaugeVec(
		"replication_consecutive_failures",
		"Failed push replication runs in a row since the last success, per replicated repository (bounded by the configured replications).",
		"repository",
	)

	// PromQL: max by (repository) (relic_replication_lag_seconds)
	replicationLag = gaugeVec(
		"replication_lag_seconds",
		"Time since a replication last ran, per replicated repository.",
		"repository",
	)
)

// JobResults are the results of a worked job.
var JobResults = []string{
	"success",
	"error",
	"cancelled",
	"snoozed",
}

// JobWorked records one job run.
func JobWorked(ctx context.Context, kind, res string, d time.Duration) {
	if !slices.Contains(JobResults, res) {
		res = "error"
	}

	jobsTotal.WithLabelValues(kind, res).Inc()
	observe(ctx, jobDuration.WithLabelValues(kind), d.Seconds())
}

// QueueStat is the state of one job kind's queue.
type QueueStat struct {
	Kind              string
	Available         int64
	Retryable         int64
	OldestAvailableAt *time.Time
}

var (
	queueKindsMu sync.Mutex
	queueKinds   []string
)

// SetQueue replaces the queue gauges.
func SetQueue(kinds []string, stats []QueueStat, now time.Time) {
	queueKindsMu.Lock()
	defer queueKindsMu.Unlock()

	byKind := make(map[string]QueueStat, len(stats))
	for _, s := range stats {
		byKind[s.Kind] = s
	}

	for _, k := range kinds {
		s := byKind[k]

		oldest := 0.0
		if s.OldestAvailableAt != nil {
			oldest = max(0, now.Sub(*s.OldestAvailableAt).Seconds())
		}

		jobsQueued.WithLabelValues(k).Set(float64(s.Available))
		jobsRetryable.WithLabelValues(k).Set(float64(s.Retryable))
		jobOldest.WithLabelValues(k).Set(oldest)

		if k == "scan_artifact" {
			scanBacklog.WithLabelValues().Set(float64(s.Available + s.Retryable))
		}
	}

	for _, k := range queueKinds {
		if slices.Contains(kinds, k) {
			continue
		}

		jobsQueued.DeleteLabelValues(k)
		jobsRetryable.DeleteLabelValues(k)
		jobOldest.DeleteLabelValues(k)

		if k == "scan_artifact" {
			scanBacklog.DeleteLabelValues()
		}
	}

	queueKinds = slices.Clone(kinds)
}

// ScanStage runs one scanner stage (catalog, match or an analyzer) in a span and
// measures it.
func ScanStage(ctx context.Context, stage string, f func(context.Context) error) error {
	switch stage {
	case "catalog", "match", "secrets", "misconfiguration", "license", "model", "reachability":
	default:
		stage = Other
	}

	ctx, span := Tracer().Start(ctx, "scan "+stage, trace.WithAttributes(attrStage.String(stage)))
	start := time.Now()
	err := f(ctx)
	observe(ctx, scanDuration.WithLabelValues(stage), time.Since(start).Seconds())
	End(span, err)

	return err
}

var scanResults = []string{
	"done",
	"partial",
	"failed",
	"skipped",
	"queued",
	"error",
}

// ScanFinished counts a finished artifact scan by its status.
func ScanFinished(status string) {
	if !slices.Contains(scanResults, status) {
		status = Other
	}

	scansTotal.WithLabelValues(status).Inc()
}

// DownloadBlocked counts a download refused by a scan policy; reason is
// "unscanned", "quarantine" or "policy".
func DownloadBlocked(reason string) {
	if reason != "unscanned" && reason != "quarantine" {
		reason = "policy"
	}

	downloadsBlocked.WithLabelValues(reason).Inc()
}

// GCRun records a garbage collection run.
func GCRun(err error, blobs int, bytes int64) {
	gcRuns.WithLabelValues(Result(err)).Inc()
	gcDeletedBlobs.WithLabelValues().Add(float64(blobs))
	gcDeletedBytes.WithLabelValues().Add(float64(max(bytes, 0)))
}

// GCTemporaryFilesDeleted counts the temporary files a garbage collection
// run deleted from the filesystem object store.
func GCTemporaryFilesDeleted(n int) {
	gcDeletedTemporaryFiles.WithLabelValues().Add(float64(n))
}

// TrashPurged counts purged trash entries.
func TrashPurged(n int) { trashPurged.WithLabelValues().Add(float64(n)) }

var (
	replMu       sync.Mutex
	replFailures = map[string]int{}
)

// ReplicationRun records a replication run of repo: its result, how
// long it took, the files it handled, and the run of failures since the
func ReplicationRun(
	ctx context.Context,
	repo string,
	err error,
	d time.Duration,
	pushed, pulled, skipped, deleted int,
) {
	c, lvs := replicationRuns.current(repo, []string{
		Result(err),
	})
	c.WithLabelValues(lvs...).Inc()
	observe(ctx, replicationDuration.WithLabelValues(), d.Seconds())
	replicationEvents.WithLabelValues("pushed").Add(float64(pushed))
	replicationEvents.WithLabelValues("pulled").Add(float64(pulled))
	replicationEvents.WithLabelValues("skipped").Add(float64(skipped))
	replicationEvents.WithLabelValues("deleted").Add(float64(deleted))
	replMu.Lock()
	defer replMu.Unlock()

	if err == nil {
		replFailures[repo] = 0
	} else {
		replFailures[repo]++
	}

	replicationFailures.WithLabelValues(repo).Set(float64(replFailures[repo]))
}

// ReplicationsConfigured drops the failure runs of every repository not in
// repos, whose replication was removed or disabled.
func ReplicationsConfigured(repos []string) {
	replMu.Lock()
	defer replMu.Unlock()

	for repo := range replFailures {
		if !slices.Contains(repos, repo) {
			delete(replFailures, repo)
			replicationFailures.DeleteLabelValues(repo)
		}
	}
}

// Inventory is what the worker counts every few minutes.
type Inventory struct {
	Repositories map[[2]string]int64
	StorageBytes, StorageBlobs, UnreferencedBytes,
	Users, Builds, TrashItems int64
	Findings     map[string]int64
	FeedFetched  map[string]time.Time
	Replications map[string]*time.Time
}

var (
	// PromQL: sum by (format, class) (relic_repositories)
	repositories = gaugeVec("repositories", "Repositories by format and class.", "format", "class")
	// PromQL: max(relic_storage_bytes)
	storageBytes = gaugeVec("storage_bytes", "Bytes of content stored (each blob once).")
	// PromQL: max(relic_storage_blobs)
	storageBlobs = gaugeVec("storage_blobs", "Blobs stored.")
	// PromQL: max(relic_unreferenced_bytes)
	unreferencedBytes = gaugeVec(
		"unreferenced_bytes",
		"Bytes of content no item references, which garbage collection will reclaim.",
	)
	// PromQL: max(relic_users)
	users = gaugeVec("users", "User accounts.")
	// PromQL: max(relic_builds)
	builds = gaugeVec("builds", "Build names with published build info.")
	// PromQL: max(relic_trash_items)
	trashItems = gaugeVec("trash_items", "Entries in the trash can.")
	// PromQL: max by (feed) (relic_cve_feed_age_seconds) > 3 * <refresh interval>   (alert RelicCVEFeedStale)
	cveFeedAge = gaugeVec("cve_feed_age_seconds", "Time since each CVE feed (epss, kev) was last read.", "feed")
	// PromQL: max(relic_scanner_db_age_seconds) > 3 * 86400   (alert RelicScanBacklog)
	scannerDBAge = gaugeVec(
		"scanner_db_age_seconds",
		"Age of the scanner's vulnerability database (from its build time).",
	)
	// PromQL: max(relic_inventory_timestamp_seconds)
	inventoryTime = gaugeVec("inventory_timestamp_seconds", "When the inventory series were last computed (Unix time).")
)

// Severities are the finding severities reported, as label values.
var Severities = []string{
	"critical",
	"high",
	"medium",
	"low",
	"negligible",
	"unknown",
}

// SetInventory replaces the inventory gauges.
func SetInventory(inv Inventory, now time.Time) {
	repositories.Reset()

	for fc, n := range inv.Repositories {
		repositories.WithLabelValues(Format(fc[0]), Class(fc[1])).Add(float64(n))
	}

	storageBytes.WithLabelValues().Set(float64(inv.StorageBytes))
	storageBlobs.WithLabelValues().Set(float64(inv.StorageBlobs))
	unreferencedBytes.WithLabelValues().Set(float64(inv.UnreferencedBytes))
	users.WithLabelValues().Set(float64(inv.Users))
	builds.WithLabelValues().Set(float64(inv.Builds))
	trashItems.WithLabelValues().Set(float64(inv.TrashItems))
	findings.Reset()

	for _, s := range Severities {
		findings.WithLabelValues(s).Set(float64(inv.Findings[s]))
	}

	cveFeedAge.Reset()

	for feed, at := range inv.FeedFetched {
		if feed == "epss" || feed == "kev" {
			cveFeedAge.WithLabelValues(feed).Set(now.Sub(at).Seconds())
		}
	}

	replicationLag.Reset()

	for repo, last := range inv.Replications {
		if last != nil {
			replicationLag.WithLabelValues(repo).Set(now.Sub(*last).Seconds())
		}
	}

	inventoryTime.WithLabelValues().Set(float64(now.Unix()))
}

var (
	scannerBuiltMu sync.Mutex
	scannerBuilt   time.Time
)

// ScannerDBBuilt records the vulnerability database's build time.
func ScannerDBBuilt(built time.Time, now time.Time) {
	scannerBuiltMu.Lock()
	defer scannerBuiltMu.Unlock()

	scannerBuilt = built
	scannerDBAge.WithLabelValues().Set(now.Sub(built).Seconds())
}

// RefreshAges moves the age gauges forward between inventories.
func RefreshAges(now time.Time) {
	scannerBuiltMu.Lock()
	defer scannerBuiltMu.Unlock()

	if !scannerBuilt.IsZero() {
		scannerDBAge.WithLabelValues().Set(now.Sub(scannerBuilt).Seconds())
	}
}
