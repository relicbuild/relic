//go:build integration

package xray

import (
	"context"
	"log/slog"
	"testing"

	"relic/internal/access/auth"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
)

func TestMain(m *testing.M) { databasetest.Main(m) }

func TestReadableInReportsARepositoryLookupFailure(t *testing.T) {
	t.Parallel()

	db := databasetest.NewDB(t)
	h := &Handler{
		repos: repository.NewService(db, objectstoretest.NewMemory(), slog.New(slog.DiscardHandler)),
		Permissions: func(context.Context) (auth.Permissions, error) {
			return auth.Permissions{}, nil
		},
	}
	ctx := auth.WithPrincipal(t.Context(), auth.Principal{
		Username: "alice",
	})

	if _, err := h.readableIn(ctx, "no-such-repository"); err != nil {
		t.Errorf("readableIn of an unknown repository = %v, want nothing readable and no error", err)
	}

	db.Close()

	if _, err := h.readableIn(ctx, "libs"); err == nil {
		t.Error("readableIn with the database gone answered as if the repository were unreadable")
	}
}
