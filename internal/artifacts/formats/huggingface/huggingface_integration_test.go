//go:build integration

package huggingface

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"relic/internal/artifacts/repository"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
)

func TestMain(m *testing.M) { databasetest.Main(m) }

func newFrozenService(t *testing.T) *Service {
	t.Helper()

	log := slog.New(slog.DiscardHandler)
	repos := repository.NewService(databasetest.NewDB(t), objectstoretest.NewMemory(), log)

	if err := repos.CreateRepository(
		t.Context(),
		repository.Repository{
			Name:   "models",
			Class:  repository.ClassLocal,
			Format: repository.FormatHuggingFace,
		},
	); err != nil {
		t.Fatal(err)
	}

	service := NewService(repos, log)
	frozen := time.Date(
		2026,
		10,
		14,
		12,
		0,
		0,
		400_000,
		time.UTC,
	)
	service.now = func() time.Time {
		return frozen
	}

	return service
}

func fileOperation(path string) Operation {
	var op Operation

	op.Key = "file"
	op.Value.Path = path
	op.Value.Encoding = "base64"
	op.Value.Content = base64.StdEncoding.EncodeToString([]byte(path))

	return op
}

func latestFiles(t *testing.T, service *Service) []string {
	t.Helper()

	latest, err := service.Latest(t.Context(), "models", "models", "org/model", "main")
	if err != nil {
		t.Fatal(err)
	}

	files, err := service.Files(t.Context(), "models", latest)
	if err != nil {
		t.Fatal(err)
	}

	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.Path)
	}

	return paths
}

func TestCreateCommitInTheSameMillisecond(t *testing.T) {
	t.Parallel()

	t.Run("concurrent commits all land", func(t *testing.T) {
		t.Parallel()

		service := newFrozenService(t)

		const commits = 6

		var wg sync.WaitGroup

		errs := make(chan error, commits)

		for i := range commits {
			wg.Go(func() {
				_, err := service.CreateCommit(
					t.Context(),
					"models",
					"models",
					"org/model",
					"main",
					[]Operation{
						fileOperation(fmt.Sprintf("file-%d.txt", i)),
					},
					"admin",
				)
				errs <- err
			})
		}

		wg.Wait()
		close(errs)

		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}

		want := make([]string, 0, commits)
		for i := range commits {
			want = append(want, fmt.Sprintf("file-%d.txt", i))
		}

		if got := latestFiles(t, service); !slices.Equal(got, want) {
			t.Errorf("latest commit holds %v, want every commit's file %v", got, want)
		}
	})

	t.Run("a failed commit keeps the one before it", func(t *testing.T) {
		t.Parallel()

		service := newFrozenService(t)

		if _, err := service.CreateCommit(
			t.Context(),
			"models",
			"models",
			"org/model",
			"main",
			[]Operation{
				fileOperation("config.json"),
			},
			"admin",
		); err != nil {
			t.Fatal(err)
		}

		var unsupported Operation

		unsupported.Key = "rename"
		unsupported.Value.Path = "weights.bin"

		if _, err := service.CreateCommit(
			t.Context(),
			"models",
			"models",
			"org/model",
			"main",
			[]Operation{
				unsupported,
			},
			"admin",
		); err == nil {
			t.Fatal("an unsupported operation was committed")
		}

		if got := latestFiles(t, service); !slices.Equal(got, []string{
			"config.json",
		}) {
			t.Errorf("after the failed commit the latest commit holds %v, want [config.json]", got)
		}
	})
}
