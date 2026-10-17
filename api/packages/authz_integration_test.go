//go:build integration

package packages

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"

	"relic/internal/artifacts/repository"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
)

func TestMain(m *testing.M) { databasetest.Main(m) }

type recordCount struct {
	records atomic.Int64
}

func (h *recordCount) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordCount) Handle(context.Context, slog.Record) error {
	h.records.Add(1)

	return nil
}

func (h *recordCount) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *recordCount) WithGroup(string) slog.Handler { return h }

func TestVisibilityLooksUpARepositoryOnceWhenTheLookupFails(t *testing.T) {
	t.Parallel()

	logged := &recordCount{}
	log := slog.New(logged)

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()

	v := &visibility{
		ctx: cancelled,
		h: &Handler{
			repos: repository.NewService(databasetest.NewDB(t), objectstoretest.NewMemory(), log),
			log:   log,
		},
		repos: map[string]visibleRepo{},
	}

	for range 3 {
		if !v.Restricted("libs-local") {
			t.Error("a repository whose lookup failed is listed without a permission check")
		}
	}

	if logged.records.Load() != 1 {
		t.Errorf("the failed lookup was logged %d times in one request, want once", logged.records.Load())
	}
}
