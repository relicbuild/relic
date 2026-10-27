package backgroundjobs

import (
	"context"
	"errors"
	"testing"

	"relic/internal/scanning/scan"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

func TestScanJobsWaitForTheDatabase(t *testing.T) {
	t.Parallel()

	noDB := func() bool {
		return false
	}

	works := map[string]func() error{
		"scan_artifact": func() error {
			return (&scanArtifactWorker{
				hasDB: noDB,
			}).Work(t.Context(), &river.Job[ScanArtifactArgs]{})
		},
		"scan_rematch": func() error {
			return (&rematchWorker{
				hasDB: noDB,
			}).Work(t.Context(), &river.Job[RematchArgs]{})
		},
		"scan_graph": func() error {
			return (&graphScanWorker{
				hasDB: noDB,
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
	failed  []string
}

func (s *recordingScanner) ScanArtifact(context.Context, string, string) error { return s.scanErr }

func (s *recordingScanner) FailArtifact(_ context.Context, sha256Hex string, failure error) error {
	s.failed = append(s.failed, sha256Hex+": "+failure.Error())

	return nil
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
				hasDB: func() bool {
					return true
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
