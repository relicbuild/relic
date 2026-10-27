package telemetry

import "slices"

// Caches are the in-memory caches counted on cache labels.
var Caches = []string{
	"repository",
	"components",
}

// PromQL: sum by (cache) (rate(relic_cache_lookups_total{result="hit"}[5m]))
//
//	/ sum by (cache) (rate(relic_cache_lookups_total[5m]))           (hit ratio)
var cacheLookups = counterVec(
	"cache_lookups_total",
	"Lookups of in-memory caches by cache (repository settings, SBOM components) and result (hit, miss).",
	"cache",
	"result",
)

// CacheLookup counts a lookup of the in-memory cache named cache.
func CacheLookup(cache string, hit bool) {
	name := cache
	if !slices.Contains(Caches, name) {
		name = Other
	}

	outcome := "miss"
	if hit {
		outcome = "hit"
	}

	cacheLookups.WithLabelValues(name, outcome).Inc()
}
