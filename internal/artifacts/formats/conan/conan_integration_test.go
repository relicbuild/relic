//go:build integration

package conan

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"relic/internal/artifacts/repository"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
)

func TestMain(m *testing.M) { databasetest.Main(m) }

func TestSearch(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	log := slog.New(slog.DiscardHandler)
	repos := repository.NewService(databasetest.NewDB(t), objectstoretest.NewMemory(), log)

	if err := repos.CreateRepository(ctx, repository.Repository{
		Name:   "conan-local",
		Class:  repository.ClassLocal,
		Format: repository.FormatConan,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := repos.Deploy(ctx, repository.DeployInput{
		Repository: "conan-local",
		Path:       "_/hello/1.0/_/abc/export/" + Manifest,
		Body:       strings.NewReader("manifest"),
		User:       "admin",
	}); err != nil {
		t.Fatal(err)
	}

	svc := NewService(repos, log)

	got, err := svc.Search(ctx, "conan-local", "hel*", true)
	if err != nil || len(got) != 1 || got[0] != "hello/1.0@_/_" {
		t.Fatalf("Search = %v %v", got, err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()

	if got, err := svc.Search(cancelled, "conan-local", "hel*", true); err == nil {
		t.Errorf("Search on a failing store = %v, nil; want the error", got)
	}
}
