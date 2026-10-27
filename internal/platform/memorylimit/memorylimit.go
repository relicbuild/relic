// Package memorylimit sizes in-memory caches from Go's soft memory limit.
package memorylimit

import (
	"math"
	"runtime/debug"
)

const floor = 1 << 20

// Share returns ceiling, or the soft memory limit divided by divisor when
// that is smaller, and never less than 1 MiB.
func Share(ceiling, divisor int64) int64 {
	return share(debug.SetMemoryLimit(-1), ceiling, divisor)
}

func share(limit, ceiling, divisor int64) int64 {
	if limit <= 0 || limit == math.MaxInt64 || divisor <= 0 {
		return ceiling
	}

	return max(min(ceiling, limit/divisor), min(ceiling, floor))
}
