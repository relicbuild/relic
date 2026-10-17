package backgroundjobs

import (
	"context"
	"testing"

	"github.com/riverqueue/river/rivertype"

	"relic/internal/platform/telemetry/telemetrytest"
)

func TestJobMetricsCountAPanickingJob(t *testing.T) {
	t.Parallel()

	labels := map[string]string{
		"kind":   "panicking_job",
		"result": "error",
	}
	before := telemetrytest.Value(t, "relic_jobs_total", labels)

	m := &jobMetrics{
		kinds: []string{
			"panicking_job",
		},
	}

	func() {
		defer func() {
			if recover() == nil {
				t.Error("the job's panic did not reach River")
			}
		}()

		_ = m.Work(t.Context(), &rivertype.JobRow{
			Kind: "panicking_job",
		}, func(context.Context) error {
			panic("the job broke")
		})
	}()

	if got := telemetrytest.Value(t, "relic_jobs_total", labels) - before; got != 1 {
		t.Errorf("a panicking job added %v to relic_jobs_total{result=error}, want 1", got)
	}
}
