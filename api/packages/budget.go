package packages

import (
	"net/http"

	"relic/internal/platform/httperror"

	"golang.org/x/sync/semaphore"
)

const bufferBudget = 1 << 30

var buffered = semaphore.NewWeighted(bufferBudget)

func reserveBuffer(w http.ResponseWriter, r *http.Request, limit, factor int64) func() {
	n := limit
	if r.ContentLength >= 0 && r.ContentLength < limit {
		n = r.ContentLength
	}

	n = min(max(n*factor, 1), bufferBudget)
	if err := buffered.Acquire(r.Context(), n); err != nil {
		httperror.Write(w, r, http.StatusServiceUnavailable, "server busy; retry later")
		return nil
	}

	return func() {
		buffered.Release(n)
	}
}
