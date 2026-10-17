package telemetry

import (
	"context"
	"time"
)

// PromQL: histogram_quantile(0.95, sum by (le, class) (rate(relic_rpm_repodata_duration_seconds_bucket[5m])))
var rpmRepodataDuration = histogramVec(
	"rpm_repodata_duration_seconds",
	"Time to generate an RPM repository's repodata, by repository class and result (success, error).",
	requestBuckets,
	"class",
	"result",
)

// RPMRepodataGenerated observes how long generating an RPM repository's
// repodata took.
func RPMRepodataGenerated(ctx context.Context, class string, err error, took time.Duration) {
	observe(ctx, rpmRepodataDuration.WithLabelValues(Class(class), Result(err)), took.Seconds())
}
