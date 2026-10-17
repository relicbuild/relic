//go:build integration

package cargo

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

func TestSearchRepositoriesReadsOnlyThePageOfCrates(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	logged := &recordCount{}
	repos := repository.NewService(databasetest.NewDB(t), objectstoretest.NewMemory(), slog.New(slog.DiscardHandler))
	svc := NewService(repos, slog.New(logged))
	local := repository.Repository{
		Name:   "cargo-local",
		Class:  repository.ClassLocal,
		Format: Format,
	}

	if err := repos.CreateRepository(ctx, local); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{
		"aaa_crate",
		"zzz_crate",
	} {
		meta := `{"name":"` + name + `","vers":"1.0.0","deps":[],"features":{},"links":null}`
		if _, err := svc.Publish(ctx, local.Name, publishBody(meta, []byte(name)), "admin"); err != nil {
			t.Fatal(err)
		}
	}

	unreadable, err := repos.FilesWithProperty(ctx, local.Name, propIndexName, "zzz_crate")
	if err != nil || len(unreadable) != 1 {
		t.Fatalf("files of zzz_crate = %v, %v", unreadable, err)
	}

	if err := repos.SetProperties(ctx, unreadable[0], repository.Properties{
		propIndex: {
			"not an index line",
		},
	}, false); err != nil {
		t.Fatal(err)
	}

	hits, total, err := svc.SearchRepositories(ctx, local, "crate", 1)
	if err != nil || len(hits) != 1 || hits[0].Name != "aaa_crate" || total != 2 {
		t.Errorf("first page = %+v of %d, %v; want aaa_crate of 2", hits, total, err)
	}

	if logged.records.Load() != 0 {
		t.Errorf("a page of one crate read the versions of a crate after it (%d warnings)", logged.records.Load())
	}

	hits, total, err = svc.SearchRepositories(ctx, local, "crate", 10)
	if err != nil || len(hits) != 1 || total != 1 {
		t.Errorf("every match = %+v of %d, %v; want the crate with a readable version alone", hits, total, err)
	}
}
