package backgroundjobs

import (
	"errors"
	"testing"

	"github.com/riverqueue/river"
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
