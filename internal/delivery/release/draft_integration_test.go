//go:build integration

package release_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"relic/internal/artifacts/repository"
	"relic/internal/delivery/release"
)

func TestConcurrentDraftUpdatesKeepEveryArtifact(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	releases, repos, _ := newReleases(t)

	const files = 8

	for i := range files {
		if _, err := repos.Deploy(ctx, repository.DeployInput{
			Repository: "libs",
			Path:       fmt.Sprintf("part-%d.bin", i),
			Body:       strings.NewReader(fmt.Sprintf("part %d", i)),
		}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := releases.Create(ctx, "", release.Spec{
		Name:    "app",
		Version: "2.0",
		Draft:   true,
	}, "admin", release.Access{}); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup

	for i := range files {
		wg.Go(func() {
			if _, err := releases.Update(ctx, "", release.Spec{
				Name:    "app",
				Version: "2.0",
				Sources: []release.Source{
					{
						Kind: release.SourceArtifacts,
						Artifacts: []release.ArtifactRef{
							{
								Path: fmt.Sprintf("libs/part-%d.bin", i),
							},
						},
					},
				},
			}, "admin", release.Access{}); err != nil {
				t.Error(err)
			}
		})
	}

	wg.Wait()

	b, err := releases.Get(ctx, "", "app", "2.0")
	if err != nil || len(b.Artifacts) != files {
		t.Errorf(
			"draft after %d concurrent updates has %d artifacts (%v), want every one",
			files,
			len(b.Artifacts),
			err,
		)
	}
}

func TestCreateHoldsTheVersionsLock(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	releases, repos, _ := newReleases(t)

	held := make(chan struct{})
	unlock := make(chan struct{})
	lockErr := make(chan error, 1)

	go func() {
		lockErr <- repos.WithLock(ctx, "release:/app/3.0", func() error {
			close(held)
			<-unlock

			return nil
		})
	}()

	<-held

	created := make(chan error, 1)

	go func() {
		_, err := releases.Create(ctx, "", release.Spec{
			Name:    "app",
			Version: "3.0",
			Draft:   true,
		}, "admin", release.Access{})
		created <- err
	}()

	select {
	case err := <-created:
		t.Errorf("Create went ahead while a change to the same version held its lock (err %v)", err)
		close(unlock)
	case <-time.After(2 * time.Second):
		close(unlock)

		if err := <-created; err != nil {
			t.Fatal(err)
		}
	}

	if err := <-lockErr; err != nil {
		t.Fatal(err)
	}
}
