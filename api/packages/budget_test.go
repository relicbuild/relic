package packages

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBufferBudget(t *testing.T) {
	t.Parallel()

	const mib = 1 << 20

	tests := []struct {
		name  string
		limit int64
		want  int64
	}{
		{
			name:  "no limit",
			limit: math.MaxInt64,
			want:  1024 * mib,
		},
		{
			name:  "512 MiB container (90%)",
			limit: 512 * mib / 10 * 9,
			want:  512 * mib / 10 * 9 / 4,
		},
		{
			name:  "a tiny container keeps the floor",
			limit: 128 * mib,
			want:  64 * mib,
		},
		{
			name:  "a large container stops at 1 GiB",
			limit: 16 * 1024 * mib,
			want:  1024 * mib,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := bufferBudget(tt.limit); got != tt.want {
				t.Errorf("bufferBudget(%d MiB) = %d MiB, want %d MiB", tt.limit/mib, got/mib, tt.want/mib)
			}
		})
	}
}

func TestBufferPoolReserve(t *testing.T) {
	t.Parallel()

	const budget = 4 << 20

	pool := newBufferPool(budget)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/", nil)
	req.ContentLength = 10

	release := pool.reserve(httptest.NewRecorder(), req, 1<<20, 2)
	if release == nil {
		t.Fatal("reserve with budget left failed")
	}

	release()

	unknownLength := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/", nil)
	unknownLength.ContentLength = -1

	release = pool.reserve(httptest.NewRecorder(), unknownLength, 512<<20, 3)
	if release == nil {
		t.Fatal("a reservation above the budget did not get the whole budget")
	}

	if pool.held.TryAcquire(1) {
		t.Error("a reservation above the budget left part of it free")
	}

	release()

	if !pool.held.TryAcquire(budget) {
		t.Fatal("budget not fully released")
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	rec := httptest.NewRecorder()
	if release := pool.reserve(rec, req.WithContext(ctx), 1<<20, 1); release != nil ||
		rec.Code != http.StatusServiceUnavailable {
		t.Errorf("reserve on a spent budget with a gone client = %v, %d; want nil, 503", release != nil, rec.Code)
	}
}
