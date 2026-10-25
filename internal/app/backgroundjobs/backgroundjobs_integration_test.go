//go:build integration

package backgroundjobs_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"uuid"

	"relic/internal/access/auth"
	"relic/internal/app/backgroundjobs"
	"relic/internal/app/config"
	"relic/internal/artifacts/repository"
	"relic/internal/delivery/replication"
	"relic/internal/platform/database"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore"
	"relic/internal/platform/telemetry"
	"relic/internal/platform/telemetry/telemetrytest"

	"github.com/riverqueue/river"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestMain(m *testing.M) { databasetest.Main(m) }

func TestEveryJobKindInserts(t *testing.T) {
	t.Parallel()

	inserter, err := backgroundjobs.NewInsertClient(databasetest.NewDB(t), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	for _, args := range []river.JobArgs{
		backgroundjobs.GCArgs{},
		backgroundjobs.TrashPurgeArgs{},
		backgroundjobs.UsageSnapshotArgs{},
		backgroundjobs.WebhookDispatchArgs{},
		backgroundjobs.AuditPurgeArgs{},
		backgroundjobs.ReplicationTickArgs{},
		backgroundjobs.ReplicationArgs{
			Repository: "libs-local",
		},
		backgroundjobs.CleanupArgs{},
		backgroundjobs.StorageCheckArgs{},
		backgroundjobs.ScanTickArgs{},
		backgroundjobs.ScanArtifactArgs{},
		backgroundjobs.ScanDBArgs{},
		backgroundjobs.RematchArgs{},
		backgroundjobs.GraphScanArgs{},
		backgroundjobs.ScanDBImportArgs{},
		backgroundjobs.ReachabilityArgs{},
		backgroundjobs.OperationalRiskArgs{},
		backgroundjobs.MaliciousArgs{},
		backgroundjobs.CVERefreshArgs{},
	} {
		if _, err := inserter.Insert(t.Context(), args, nil); err != nil {
			t.Errorf("insert %s: %v", args.Kind(), err)
		}
	}
}

// Not parallel: it installs the global tracer provider River's middleware uses.
func TestWorkerTelemetry(t *testing.T) {
	spans := tracetest.NewSpanRecorder()
	previous := otel.GetTracerProvider()

	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)))
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
	})

	ctx := t.Context()
	db := databasetest.NewDB(t)
	log := slog.New(slog.DiscardHandler)
	storeDir := t.TempDir()

	blobs, err := objectstore.NewFilesystem(storeDir)
	if err != nil {
		t.Fatal(err)
	}

	if err := blobs.Put(ctx, objectstore.TemporaryKeyPrefix+"abandoned", strings.NewReader("x"), 1); err != nil {
		t.Fatal(err)
	}

	abandoned := time.Now().Add(-2 * objectstore.TemporaryTTL)
	if err := os.Chtimes(filepath.Join(storeDir, "temporary", "abandoned"), abandoned, abandoned); err != nil {
		t.Fatal(err)
	}

	repos := repository.NewService(db, blobs, log)
	if err := repos.CreateRepository(
		ctx,
		repository.Repository{
			Name:   "libs-local",
			Class:  "local",
			Format: "generic",
		},
	); err != nil {
		t.Fatal(err)
	}

	hash, err := auth.HashPassword("pw")
	if err != nil {
		t.Fatal(err)
	}

	if err := db.CreateUser(
		ctx,
		database.CreateUserParams{
			ID:           uuid.NewV7(),
			Username:     "alice",
			PasswordHash: hash,
		},
	); err != nil {
		t.Fatal(err)
	}

	inserter, err := backgroundjobs.NewInsertClient(db, log)
	if err != nil {
		t.Fatal(err)
	}

	reqCtx, request := otel.Tracer("test").Start(ctx, "request")
	if _, err := inserter.Insert(reqCtx, backgroundjobs.GCArgs{}, nil); err != nil {
		t.Fatal(err)
	}

	request.End()

	gcOK := telemetrytest.Value(t, "relic_jobs_total", map[string]string{
		"kind":   "gc",
		"result": "success",
	})
	gcRuns := telemetrytest.Value(t, "relic_gc_runs_total", map[string]string{
		"result": "success",
	})
	sweptBefore := telemetrytest.Value(t, "relic_gc_deleted_temporary_files_total", nil)

	worker, err := backgroundjobs.NewWorkerClient(db, repos, replication.NewPusher(db, repos, nil, log), nil, nil,
		config.GarbageCollection{
			Interval:    time.Hour,
			GracePeriod: time.Hour,
		}, config.Trash{
			Retention: time.Hour,
		}, config.Audit{}, log)
	if err != nil {
		t.Fatal(err)
	}

	if err := worker.Start(ctx); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(30 * time.Second)
	for telemetrytest.Value(t, "relic_jobs_total", map[string]string{
		"kind":   "gc",
		"result": "success",
	}) <= gcOK {
		if time.Now().After(deadline) {
			t.Fatal("the gc job was not worked in 30s")
		}

		time.Sleep(100 * time.Millisecond)
	}

	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()

	if err := worker.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}

	if telemetrytest.Value(t, "relic_gc_runs_total", map[string]string{
		"result": "success",
	}) <= gcRuns {
		t.Error("relic_gc_runs_total did not move")
	}

	if swept := telemetrytest.Value(t, "relic_gc_deleted_temporary_files_total", nil) - sweptBefore; swept != 1 {
		t.Errorf("relic_gc_deleted_temporary_files_total moved by %v, want 1", swept)
	}

	if _, err := blobs.Head(ctx, objectstore.TemporaryKeyPrefix+"abandoned"); !errors.Is(err, objectstore.ErrNotFound) {
		t.Errorf("the abandoned upload survived the GC run: %v", err)
	}

	if telemetrytest.Value(t, "relic_job_duration_seconds", map[string]string{
		"kind": "gc",
	}) < 1 {
		t.Error("gc duration not observed")
	}

	linked := false

	for _, s := range spans.Ended() {
		if !strings.HasPrefix(s.Name(), "river.work/gc") {
			continue
		}

		for _, l := range s.Links() {
			linked = linked || l.SpanContext.TraceID() == request.SpanContext().TraceID()
		}
	}

	if !linked {
		t.Error("no river.work/gc span links to the span that queued the job")
	}

	inv, err := backgroundjobs.Count(ctx, db, repos)
	if err != nil {
		t.Fatal(err)
	}

	if inv.Repositories[[2]string{
		"generic",
		"local",
	}] != 1 || inv.Users != 1 {
		t.Errorf("inventory = %+v", inv)
	}

	telemetry.SetInventory(inv, time.Now())

	if telemetrytest.Value(t, "relic_repositories", map[string]string{
		"format": "generic",
		"class":  "local",
	}) != 1 {
		t.Error("relic_repositories not set from the inventory")
	}

	if _, err := inserter.Insert(ctx, backgroundjobs.GCArgs{}, nil); err != nil {
		t.Fatal(err)
	}

	queues, err := db.JobQueues(ctx)
	if err != nil {
		t.Fatal(err)
	}

	stats := make([]telemetry.QueueStat, 0, 1+len(queues))
	stats = append(stats, telemetry.QueueStat{
		Kind:      "not-a-kind",
		Available: 5,
	})

	for _, q := range queues {
		stats = append(stats, telemetry.QueueStat(q))
	}

	telemetry.SetQueue(backgroundjobs.Kinds(false), stats, time.Now())

	if telemetrytest.Value(t, "relic_jobs_queued", map[string]string{
		"kind": "gc",
	}) != 1 {
		t.Errorf(
			"relic_jobs_queued{kind=gc} = %v",
			telemetrytest.Value(t, "relic_jobs_queued", map[string]string{
				"kind": "gc",
			}),
		)
	}

	if telemetrytest.Value(t, "relic_jobs_queued", map[string]string{
		"kind": "not-a-kind",
	}) != 0 {
		t.Error("an unregistered kind became a label value")
	}
}

func TestWorkerFreesTheKindOfAJobLeftRunningByACrash(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := databasetest.NewDB(t)
	log := slog.New(slog.DiscardHandler)

	inserter, err := backgroundjobs.NewInsertClient(db, log)
	if err != nil {
		t.Fatal(err)
	}

	abandoned := map[string]time.Duration{
		backgroundjobs.CleanupArgs{}.Kind():      30 * time.Minute,
		backgroundjobs.StorageCheckArgs{}.Kind(): 3 * time.Hour,
	}
	ids := map[string]int64{}

	for _, args := range []river.JobArgs{
		backgroundjobs.CleanupArgs{},
		backgroundjobs.StorageCheckArgs{},
	} {
		res, err := inserter.Insert(ctx, args, nil)
		if err != nil {
			t.Fatal(err)
		}

		ids[args.Kind()] = res.Job.ID
		markRunningSince(t, db, res.Job.ID, abandoned[args.Kind()])
	}

	blobs, err := objectstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	repos := repository.NewService(db, blobs, log)

	worker, err := backgroundjobs.NewWorkerClient(db, repos, replication.NewPusher(db, repos, nil, log), nil, nil,
		config.GarbageCollection{
			Interval:    time.Hour,
			GracePeriod: time.Hour,
		}, config.Trash{
			Retention: time.Hour,
		}, config.Audit{}, log)
	if err != nil {
		t.Fatal(err)
	}

	if err := worker.Start(ctx); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()

		_ = worker.Stop(stopCtx)
	})

	for kind, id := range ids {
		waitForJobState(t, db, kind, id, "discarded")

		res, err := inserter.Insert(ctx, kindArgs(kind), nil)
		if err != nil {
			t.Fatal(err)
		}

		if res.UniqueSkippedAsDuplicate || res.Job.ID == id {
			t.Errorf("queueing %s after the rescue returned the abandoned job %d", kind, id)
		}
	}
}

func kindArgs(kind string) river.JobArgs {
	if kind == (backgroundjobs.CleanupArgs{}).Kind() {
		return backgroundjobs.CleanupArgs{}
	}

	return backgroundjobs.StorageCheckArgs{}
}

func markRunningSince(t *testing.T, db *database.DB, id int64, ago time.Duration) {
	t.Helper()

	query := fmt.Sprintf(`UPDATE river_job
SET state = 'running', attempt = 1, attempted_at = now() - interval '%d seconds',
    attempted_by = ARRAY['crashed-worker']
WHERE id = %d`, int(ago.Seconds()), id)
	if db.SQLite() {
		query = fmt.Sprintf(`UPDATE river_job
SET state = 'running', attempt = 1, attempted_at = datetime('now', '-%d seconds', 'subsec'),
    attempted_by = json_array('crashed-worker')
WHERE id = %d`, int(ago.Seconds()), id)
	}

	rows, err := db.Query(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}

	rows.Close()
}

func waitForJobState(t *testing.T, db *database.DB, kind string, id int64, state string) {
	t.Helper()

	deadline := time.Now().Add(time.Minute)

	for {
		jobs, err := db.Jobs(t.Context(), kind, "", 10)
		if err != nil {
			t.Fatal(err)
		}

		for _, job := range jobs {
			if job.ID == id && job.State == state {
				return
			}
		}

		if time.Now().After(deadline) {
			t.Fatalf("%s job %d not %s after a minute: %+v", kind, id, state, jobs)
		}

		time.Sleep(200 * time.Millisecond)
	}
}
