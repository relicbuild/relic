package packages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Not parallel: it spends the process-wide budget.
func TestReserveBuffer(t *testing.T) {
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/", nil)
	req.ContentLength = 10

	release := reserveBuffer(httptest.NewRecorder(), req, 1<<20, 2)
	if release == nil {
		t.Fatal("reserve with budget left failed")
	}

	release()

	if !buffered.TryAcquire(bufferBudget) {
		t.Fatal("budget not fully released")
	}
	defer buffered.Release(bufferBudget)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	rec := httptest.NewRecorder()
	if release := reserveBuffer(rec, req.WithContext(ctx), 1<<20, 1); release != nil ||
		rec.Code != http.StatusServiceUnavailable {
		t.Errorf("reserve on a spent budget with a gone client = %v, %d; want nil, 503", release != nil, rec.Code)
	}
}
