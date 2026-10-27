package telemetry

import "slices"

// PackumentCaches are the npm packument caches: the versions read from a
// cached packument (index) and the documents served from it (document).
var PackumentCaches = []string{
	"index",
	"document",
}

var (
	// PromQL: sum by (cache) (rate(relic_packument_cache_total{result="miss"}[5m]))
	//   / sum by (cache) (rate(relic_packument_cache_total[5m]))   (miss ratio)
	packumentCache = counterVec(
		"packument_cache_total",
		"Lookups of npm packument caches, by cache (index, document) and result (hit, miss).",
		"cache",
		"result",
	)

	packumentCacheBytes = gaugeVec(
		"packument_cache_bytes",
		"Bytes held by the npm packument caches, by cache (index, document).",
		"cache",
	)
)

func packumentCacheName(name string) string {
	if slices.Contains(PackumentCaches, name) {
		return name
	}

	return Other
}

// PackumentCacheLookup counts a lookup of an npm packument cache.
func PackumentCacheLookup(cache, outcome string) {
	packumentCache.WithLabelValues(packumentCacheName(cache), result(outcome)).Inc()
}

// PackumentCacheSize records the bytes an npm packument cache holds.
func PackumentCacheSize(cache string, bytes int64) {
	packumentCacheBytes.WithLabelValues(packumentCacheName(cache)).Set(float64(bytes))
}
