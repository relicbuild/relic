//go:build integration

package backgroundjobs

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"relic/internal/artifacts/repository"
	"relic/internal/platform/database"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore"
	"relic/internal/scanning/scan"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

func TestGCLogsBlobStatsErrors(t *testing.T) {
	t.Parallel()

	db := databasetest.NewDB(t)

	blobs, err := objectstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	var logged bytes.Buffer

	log := slog.New(slog.NewTextHandler(&logged, nil))
	worker := &gcWorker{
		db:    db,
		repos: repository.NewService(db, blobs, log),
		grace: time.Hour,
		log:   log,
	}

	db.Close()

	if err := worker.Work(t.Context(), &river.Job[GCArgs]{}); err == nil {
		t.Fatal("Work on a closed database succeeded")
	}

	if !strings.Contains(logged.String(), "read blob stats around garbage collection") {
		t.Errorf("the blob stats error was not logged: %s", logged.String())
	}
}

func TestReplicationTicksRunConcurrently(t *testing.T) {
	t.Parallel()

	db := databasetest.NewDB(t)
	db.Close()

	worker := &tickWorker{
		db:  db,
		log: slog.New(slog.DiscardHandler),
	}

	var wg sync.WaitGroup

	for range 4 {
		wg.Go(func() {
			_ = worker.Work(t.Context(), &river.Job[ReplicationTickArgs]{})
		})
	}

	wg.Wait()
}

type failingInserter struct {
	inserted []string
	failAt   int
}

func (f *failingInserter) Insert(
	_ context.Context,
	args river.JobArgs,
	_ *river.InsertOpts,
) (*rivertype.JobInsertResult, error) {
	if len(f.inserted) == f.failAt {
		return nil, errors.New("queue unavailable")
	}

	f.inserted = append(f.inserted, args.(ScanArtifactArgs).SHA256)

	return &rivertype.JobInsertResult{}, nil
}

func TestQueueScansRequeuesTheClaimsItCouldNotQueue(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := databasetest.NewDB(t)

	blobs, err := objectstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	repos := repository.NewService(db, blobs, slog.New(slog.DiscardHandler))
	if err := repos.CreateRepository(ctx, repository.Repository{
		Name:   "libs-local",
		Class:  "local",
		Format: "generic",
	}); err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{
		"a.jar",
		"b.jar",
		"c.jar",
	} {
		if _, err := repos.Deploy(ctx, repository.DeployInput{
			Repository: "libs-local",
			Path:       p,
			Body:       strings.NewReader(p),
			User:       "admin",
		}); err != nil {
			t.Fatal(err)
		}
	}

	now := time.Now()

	claimed, err := db.ClaimUnscanned(ctx, database.ClaimUnscannedParams{
		Now:         now,
		NamePattern: scan.ScannablePattern,
		RetryBefore: now,
		MaxRows:     10,
	})
	if err != nil || len(claimed) != 3 {
		t.Fatalf("ClaimUnscanned = %v, %v; want three rows", claimed, err)
	}

	client := &failingInserter{
		failAt: 1,
	}
	if err := queueScans(ctx, db, client, claimed); err == nil {
		t.Fatal("queueScans succeeded with a failing inserter")
	}

	for _, r := range claimed[1:] {
		row, err := db.GetScanArtifact(ctx, r.Sha256)
		if err != nil {
			t.Fatal(err)
		}

		if row.Status != scan.StatusQueued || row.Reason != scanWaitingReason {
			t.Errorf("scan %s = %s %q; want queued and waiting", r.Sha256, row.Status, row.Reason)
		}
	}

	later := time.Now().Add(time.Hour)

	again, err := db.ClaimUnscanned(ctx, database.ClaimUnscannedParams{
		Now:         later,
		NamePattern: scan.ScannablePattern,
		RetryBefore: later,
		MaxRows:     10,
	})
	if err != nil || len(again) != 2 {
		t.Errorf("ClaimUnscanned after the failure = %v, %v; want the two unqueued rows", again, err)
	}
}
