package backgroundjobs

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

	"relic/internal/artifacts/repository"
	"relic/internal/platform/database"
	"relic/internal/platform/telemetry"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/riverqueue/rivercontrib/otelriver"
)

func middleware(kinds []string) []rivertype.Middleware {
	return []rivertype.Middleware{
		otelriver.NewMiddleware(
			&otelriver.MiddlewareConfig{
				EnableTracePropagation:      true,
				EnableWorkSpanJobKindSuffix: true,
			},
		),
		&jobMetrics{
			kinds: kinds,
		},
	}
}

type jobMetrics struct {
	river.MiddlewareDefaults
	kinds []string
}

func (m *jobMetrics) Work(ctx context.Context, job *rivertype.JobRow, doInner func(context.Context) error) error {
	start := time.Now()

	kind := job.Kind
	if !slices.Contains(m.kinds, kind) {
		kind = telemetry.Other
	}

	defer func() {
		if p := recover(); p != nil {
			telemetry.JobWorked(ctx, kind, "error", time.Since(start))
			panic(p)
		}
	}()

	err := doInner(ctx)

	telemetry.JobWorked(ctx, kind, jobResult(err), time.Since(start))

	return err
}

func jobResult(err error) string {
	var (
		cancel *river.JobCancelError
		snooze *river.JobSnoozeError
	)
	switch {
	case err == nil:
		return "success"
	case errors.As(err, &cancel):
		return "cancelled"
	case errors.As(err, &snooze):
		return "snoozed"
	default:
		return "error"
	}
}

const (
	queueInterval     = 15 * time.Second
	inventoryInterval = 5 * time.Minute
)

// Monitor keeps the worker's gauges current until ctx ends.
type Monitor struct {
	DB           *database.DB
	Repositories *repository.Service
	Kinds        []string
	Log          *slog.Logger
}

// Run updates the gauges at once, then on their intervals.
func (m Monitor) Run(ctx context.Context) {
	queue := time.NewTicker(queueInterval)
	defer queue.Stop()

	inventory := time.NewTicker(inventoryInterval)
	defer inventory.Stop()

	m.queues(ctx)
	m.inventory(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-queue.C:
			m.queues(ctx)
			telemetry.RefreshAges(time.Now())
		case <-inventory.C:
			m.inventory(ctx)
		}
	}
}

func (m Monitor) queues(ctx context.Context) {
	rows, err := m.DB.JobQueues(ctx)
	if err != nil {
		m.Log.WarnContext(ctx, "job queue metrics", "error", err)
		return
	}

	stats := make([]telemetry.QueueStat, 0, len(rows))
	for _, r := range rows {
		stats = append(stats, telemetry.QueueStat(r))
	}

	telemetry.SetQueue(m.Kinds, stats, time.Now())
}

func (m Monitor) inventory(ctx context.Context) {
	inv, err := Count(ctx, m.DB, m.Repositories)
	if err != nil {
		m.Log.WarnContext(ctx, "inventory metrics", "error", err)
		return
	}

	telemetry.SetInventory(inv, time.Now())
}

// Count computes the inventory.
func Count(ctx context.Context, db *database.DB, repos *repository.Service) (telemetry.Inventory, error) {
	inv := telemetry.Inventory{
		Repositories: map[[2]string]int64{},
		Findings:     map[string]int64{},
		FeedFetched:  map[string]time.Time{},
		Replications: map[string]*time.Time{},
	}

	rs, err := repos.ListRepositories(ctx, "", "")
	if err != nil {
		return inv, err
	}

	for _, r := range rs {
		inv.Repositories[[2]string{
			string(r.Format),
			string(r.Class),
		}]++
	}

	b, err := db.BlobStats(ctx)
	if err != nil {
		return inv, err
	}

	inv.StorageBytes, inv.StorageBlobs, inv.UnreferencedBytes = b.Bytes, b.Blobs, b.UnreferencedBytes
	if inv.Users, err = db.CountUsers(ctx); err != nil {
		return inv, err
	}

	names, err := db.ListBuildNames(ctx, "")
	if err != nil {
		return inv, err
	}

	inv.Builds = int64(len(names))

	_, trashItems, err := repos.TrashEntries(ctx, repository.TrashQuery{})
	if err != nil {
		return inv, err
	}

	inv.TrashItems = trashItems

	for _, s := range telemetry.Severities {
		for _, spelling := range []string{
			strings.ToUpper(s[:1]) + s[1:],
			s,
		} {
			n, err := db.CountFindings(ctx, database.CountFindingsParams{
				Severity: spelling,
			})
			if err != nil {
				return inv, err
			}

			inv.Findings[s] += n
		}
	}

	for _, feed := range []string{
		"epss",
		"kev",
	} {
		f, err := db.GetCVEFeed(ctx, feed)
		switch {
		case err == nil:
			inv.FeedFetched[feed] = f.Fetched
		case !errors.Is(database.NotFound(err), database.ErrNotFound):
			return inv, err
		}
	}

	reps, err := db.ListReplications(ctx)
	if err != nil {
		return inv, err
	}

	for _, r := range reps {
		inv.Replications[r.Repository] = r.LastRun
	}

	return inv, nil
}
