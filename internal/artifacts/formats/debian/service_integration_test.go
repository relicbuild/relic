//go:build integration

package debian

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"relic/internal/artifacts/repository"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
)

func TestMain(m *testing.M) { databasetest.Main(m) }

func TestDistributionMemberErrors(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	log := slog.New(slog.DiscardHandler)
	repos := repository.NewService(databasetest.NewDB(t), objectstoretest.NewMemory(), log)
	local := repository.Repository{
		Name:   "deb-local",
		Class:  repository.ClassLocal,
		Format: repository.FormatDebian,
	}

	if err := repos.CreateRepository(ctx, local); err != nil {
		t.Fatal(err)
	}

	svc := NewService(repos, nil, log)

	if _, err := svc.Distribution(ctx, local, "bookworm"); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("a distribution no member has = %v, want ErrNotFound", err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()

	_, err := svc.Distribution(cancelled, local, "bookworm")
	if err == nil || errors.Is(err, repository.ErrNotFound) {
		t.Errorf("a distribution every member failed to read = %v, want the members' error", err)
	}
}
