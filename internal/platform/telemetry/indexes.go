package telemetry

import (
	"context"
	"time"
)

var (
	// PromQL: histogram_quantile(0.95, sum by (le, format, class) (rate(relic_index_build_duration_seconds_bucket[5m])))
	indexBuildDuration = histogramVec(
		"index_build_duration_seconds",
		"Time to generate a repository's indexes (Debian distributions, RPM repodata, Helm indexes), by format, repository class and result (success, error).",
		requestBuckets,
		"format",
		"class",
		"result",
	)

	// PromQL: sum by (format) (rate(relic_index_cache_total{result="miss"}[5m]))   (requests that waited for a build)
	indexCache = counterVec(
		"index_cache_total",
		"Lookups of generated indexes: hit (served fresh), stale (served while a rebuild runs), miss (waited for a build).",
		"format",
		"result",
	)

	indexCacheBytes = gaugeVec(
		"index_cache_bytes",
		"Bytes the kept generated indexes take, by format.",
		"format",
	)
)

// IndexBuilt observes how long generating a repository's indexes took.
func IndexBuilt(ctx context.Context, format, class string, err error, took time.Duration) {
	observe(ctx, indexBuildDuration.WithLabelValues(Format(format), Class(class), Result(err)), took.Seconds())
}

// IndexCacheLookup counts a lookup of generated indexes.
func IndexCacheLookup(format, outcome string) {
	indexCache.WithLabelValues(Format(format), result(outcome)).Inc()
}

// IndexCacheResized adds delta to the bytes format's kept indexes take.
func IndexCacheResized(format string, delta int64) {
	indexCacheBytes.WithLabelValues(Format(format)).Add(float64(delta))
}
