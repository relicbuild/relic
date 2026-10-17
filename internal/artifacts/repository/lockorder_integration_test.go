//go:build integration

package repository

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

const lockOrderIterations = 200

func concurrently(fns ...func() error) []error {
	errs := make([]error, len(fns))

	var (
		wg    sync.WaitGroup
		start = make(chan struct{})
	)

	for i, fn := range fns {
		wg.Go(func() {
			<-start

			errs[i] = fn()
		})
	}

	close(start)
	wg.Wait()

	return errs
}

func deployContent(s *Service, path, content string) func() error {
	return func() error {
		_, err := s.Deploy(
			context.Background(),
			DeployInput{
				Repository: "generic-local",
				Path:       path,
				Body:       strings.NewReader(content),
				User:       "admin",
			},
		)

		return err
	}
}

func TestRedeployRacingDeleteOfThePath(t *testing.T) {
	t.Parallel()

	s, _, db := newTestService(t)

	for i := range lockOrderIterations {
		deploy(t, s, "race/p.txt", "same")

		errs := concurrently(
			deployContent(s, "race/p.txt", "same"),
			func() error {
				err := s.Delete(context.Background(), "generic-local", "race/p.txt")
				if errors.Is(err, ErrNotFound) {
					return nil
				}

				return err
			},
		)
		for _, err := range errs {
			if err != nil {
				t.Fatalf("iteration %d: %v", i, err)
			}
		}

		want := int64(0)
		if _, err := s.Stat(t.Context(), "generic-local", "race/p.txt"); err == nil {
			want = 1
		}

		if got := refcount(t, db, "same"); got != want {
			t.Fatalf("iteration %d: refcount = %d, want %d", i, got, want)
		}

		if err := s.Delete(t.Context(), "generic-local", "race"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOverwritesSwappingContentBetweenPaths(t *testing.T) {
	t.Parallel()

	s, _, db := newTestService(t)

	for i := range lockOrderIterations {
		deploy(t, s, "swap/p.txt", "a")
		deploy(t, s, "swap/q.txt", "b")

		errs := concurrently(
			deployContent(s, "swap/p.txt", "b"),
			deployContent(s, "swap/q.txt", "a"),
		)
		for _, err := range errs {
			if err != nil {
				t.Fatalf("iteration %d: %v", i, err)
			}
		}

		if got := refcount(t, db, "a"); got != 1 {
			t.Fatalf("iteration %d: refcount(a) = %d, want 1", i, got)
		}

		if got := refcount(t, db, "b"); got != 1 {
			t.Fatalf("iteration %d: refcount(b) = %d, want 1", i, got)
		}
	}
}
