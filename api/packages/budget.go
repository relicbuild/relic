package packages

import (
	"math"
	"net/http"

	"relic/internal/platform/httperror"

	"golang.org/x/sync/semaphore"
)

const (
	minBufferBudget int64 = 64 << 20
	maxBufferBudget int64 = 1 << 30
)

func bufferBudget(memoryLimit int64) int64 {
	if memoryLimit <= 0 || memoryLimit == math.MaxInt64 {
		return maxBufferBudget
	}

	return min(max(memoryLimit/4, minBufferBudget), maxBufferBudget)
}

type bufferPool struct {
	budget int64
	held   *semaphore.Weighted
}

func newBufferPool(budget int64) *bufferPool {
	return &bufferPool{
		budget: budget,
		held:   semaphore.NewWeighted(budget),
	}
}

func (b *bufferPool) reserve(w http.ResponseWriter, r *http.Request, limit, factor int64) func() {
	n := limit
	if r.ContentLength >= 0 && r.ContentLength < limit {
		n = r.ContentLength
	}

	n = min(max(n*factor, 1), b.budget)
	if err := b.held.Acquire(r.Context(), n); err != nil {
		httperror.Write(w, r, http.StatusServiceUnavailable, "server busy; retry later")
		return nil
	}

	return func() {
		b.held.Release(n)
	}
}
