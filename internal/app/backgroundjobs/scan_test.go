package backgroundjobs

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"uuid"

	"relic/internal/scanning/scan"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

func TestScanJobsWaitForTheDatabase(t *testing.T) {
	t.Parallel()

	noDB := databaseAvailability{
		hasDB: func() bool {
			return false
		},
		downloadFailed: &atomic.Bool{},
	}

	works := map[string]func() error{
		"scan_artifact": func() error {
			return (&scanArtifactWorker{
				vulnerabilityDatabase: noDB,
			}).Work(t.Context(), &river.Job[ScanArtifactArgs]{})
		},
		"scan_rematch": func() error {
			return (&rematchWorker{
				hasDB: noDB.hasDB,
			}).Work(t.Context(), &river.Job[RematchArgs]{})
		},
		"scan_graph": func() error {
			return (&graphScanWorker{
				vulnerabilityDatabase: noDB,
			}).Work(t.Context(), &river.Job[GraphScanArgs]{})
		},
	}
	for name, work := range works {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var snooze *river.JobSnoozeError
			if err := work(); !errors.As(err, &snooze) || snooze.Duration != dbWait {
				t.Errorf("Work = %v, want a snooze of %v", err, dbWait)
			}
		})
	}
}

type recordingScanner struct {
	scanErr error
	ran     bool
	failed  []string
}

func (s *recordingScanner) ScanArtifact(context.Context, string, string) error {
	s.ran = true

	return s.scanErr
}

func (s *recordingScanner) FailArtifact(_ context.Context, sha256Hex string, failure error) error {
	s.failed = append(s.failed, sha256Hex+": "+failure.Error())

	return nil
}

type recordingGraphScanner struct {
	ran     bool
	failure error
}

func (s *recordingGraphScanner) RunGraph(context.Context, uuid.UUID, bool) error {
	s.ran = true

	return nil
}

func (s *recordingGraphScanner) FailGraph(_ context.Context, _ uuid.UUID, failure error) error {
	s.failure = failure

	return nil
}

func TestScansFailWhenNoDatabaseWillArrive(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		hasDB          bool
		downloadsOff   bool
		downloadFailed bool
		wantFailure    error
		wantSnooze     bool
		wantRun        bool
	}{
		{
			name:         "downloads are off and none was imported",
			downloadsOff: true,
			wantFailure:  errDatabaseOff,
		},
		{
			name:           "the download failed",
			downloadFailed: true,
			wantFailure:    errDatabaseDownloadFailed,
		},
		{
			name:       "a download may still bring one",
			wantSnooze: true,
		},
		{
			name:         "an imported database with downloads off",
			hasDB:        true,
			downloadsOff: true,
			wantRun:      true,
		},
		{
			name:           "an older database after a failed download",
			hasDB:          true,
			downloadFailed: true,
			wantRun:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			downloadFailed := &atomic.Bool{}
			downloadFailed.Store(tt.downloadFailed)

			vulnerabilityDatabase := databaseAvailability{
				hasDB: func() bool {
					return tt.hasDB
				},
				downloadsOff:   tt.downloadsOff,
				downloadFailed: downloadFailed,
			}

			graphs := &recordingGraphScanner{}
			graphErr := (&graphScanWorker{
				scans:                 graphs,
				vulnerabilityDatabase: vulnerabilityDatabase,
				log:                   slog.New(slog.DiscardHandler),
			}).Work(t.Context(), &river.Job[GraphScanArgs]{
				JobRow: &rivertype.JobRow{
					Attempt:     1,
					MaxAttempts: 3,
				},
			})

			if _, snoozed := errors.AsType[*river.JobSnoozeError](graphErr); snoozed != tt.wantSnooze {
				t.Errorf("graph err = %v, want snooze %v", graphErr, tt.wantSnooze)
			}

			if !tt.wantSnooze && graphErr != nil {
				t.Errorf("graph err = %v", graphErr)
			}

			if !errors.Is(graphs.failure, tt.wantFailure) {
				t.Errorf("graph failure = %v, want %v", graphs.failure, tt.wantFailure)
			}

			if graphs.ran != tt.wantRun {
				t.Errorf("graph ran = %v, want %v", graphs.ran, tt.wantRun)
			}

			artifacts := &recordingScanner{}
			artifactErr := (&scanArtifactWorker{
				scans:                 artifacts,
				vulnerabilityDatabase: vulnerabilityDatabase,
				log:                   slog.New(slog.DiscardHandler),
			}).Work(t.Context(), &river.Job[ScanArtifactArgs]{
				JobRow: &rivertype.JobRow{
					Attempt:     1,
					MaxAttempts: 3,
				},
				Args: ScanArtifactArgs{
					SHA256: "abc",
				},
			})

			if _, snoozed := errors.AsType[*river.JobSnoozeError](artifactErr); snoozed != tt.wantSnooze {
				t.Errorf("artifact err = %v, want snooze %v", artifactErr, tt.wantSnooze)
			}

			if !tt.wantSnooze && artifactErr != nil {
				t.Errorf("artifact err = %v", artifactErr)
			}

			var wantFailed []string
			if tt.wantFailure != nil {
				wantFailed = []string{
					"abc: match: " + tt.wantFailure.Error(),
				}
			}

			if !slices.Equal(artifacts.failed, wantFailed) {
				t.Errorf("failed artifact scans = %q, want %q", artifacts.failed, wantFailed)
			}

			if artifacts.ran != tt.wantRun {
				t.Errorf("artifact ran = %v, want %v", artifacts.ran, tt.wantRun)
			}
		})
	}
}

func TestScanDatabaseWorkerRecordsTheDownload(t *testing.T) {
	t.Parallel()

	failing := filepath.Join(t.TempDir(), "grype")
	//nolint:gosec // G306: an executable test script
	if err := os.WriteFile(failing, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	downloadFailed := &atomic.Bool{}
	worker := &scanDBWorker{
		grype: scan.Grype{
			Exec: scan.Exec{
				Path: failing,
			},
			DatabaseDir: t.TempDir(),
		},
		downloadFailed: downloadFailed,
		log:            slog.New(slog.DiscardHandler),
	}
	job := &river.Job[ScanDBArgs]{
		JobRow: &rivertype.JobRow{
			Attempt:     1,
			MaxAttempts: 3,
		},
	}

	if err := worker.Work(t.Context(), job); err == nil {
		t.Fatal("a failing download reported no error")
	}

	if downloadFailed.Load() {
		t.Error("a download with attempts left counts as failed")
	}

	job.Attempt = 3

	if err := worker.Work(t.Context(), job); err == nil {
		t.Fatal("a failing download reported no error")
	}

	if !downloadFailed.Load() {
		t.Error("the failed download was not recorded")
	}

	succeeding := filepath.Join(t.TempDir(), "grype")
	//nolint:gosec // G306: an executable test script
	if err := os.WriteFile(succeeding, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	worker.grype.Path = succeeding

	if err := worker.Work(t.Context(), job); err != nil {
		t.Fatal(err)
	}

	if downloadFailed.Load() {
		t.Error("the finished download still counts as failed")
	}
}

func TestScanArtifactWorkerRecordsTheLastFailedAttempt(t *testing.T) {
	t.Parallel()

	matchFailed := errors.New("match: grype: exit status 1")

	tests := []struct {
		name       string
		scanErr    error
		attempt    int
		wantFailed int
	}{
		{
			name:    "an earlier attempt leaves it for the retry",
			scanErr: matchFailed,
			attempt: 1,
		},
		{
			name:       "the last attempt records the failure",
			scanErr:    matchFailed,
			attempt:    3,
			wantFailed: 1,
		},
		{
			name:    "a scan that succeeds on the last attempt",
			attempt: 3,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scanner := &recordingScanner{
				scanErr: tt.scanErr,
			}

			err := (&scanArtifactWorker{
				scans: scanner,
				vulnerabilityDatabase: databaseAvailability{
					hasDB: func() bool {
						return true
					},
					downloadFailed: &atomic.Bool{},
				},
			}).Work(t.Context(), &river.Job[ScanArtifactArgs]{
				JobRow: &rivertype.JobRow{
					Attempt:     tt.attempt,
					MaxAttempts: 3,
				},
				Args: ScanArtifactArgs{
					SHA256: "abc",
				},
			})
			if !errors.Is(err, tt.scanErr) {
				t.Errorf("Work = %v, want %v", err, tt.scanErr)
			}

			if len(scanner.failed) != tt.wantFailed {
				t.Errorf("failed scans recorded = %v, want %d", scanner.failed, tt.wantFailed)
			}
		})
	}
}

func TestScanQueueRunsAsManyJobsAsScannerSlots(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		slots *scan.ScannerSlots
		want  int
	}{
		{
			name:  "a small budget runs one",
			slots: scan.NewScannerSlots(256 << 20),
			want:  1,
		},
		{
			name:  "a large budget runs two",
			slots: scan.NewScannerSlots(4 << 30),
			want:  2,
		},
		{
			name: "no budget runs two",
			want: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := scanWorkers(&Scanning{
				Slots: tt.slots,
			}); got != tt.want {
				t.Errorf("scanWorkers = %d, want %d", got, tt.want)
			}
		})
	}
}
