//go:build integration

package debian

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"relic/internal/artifacts/repository"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
)

func TestMain(m *testing.M) { databasetest.Main(m) }

type debianFixture struct {
	repos   *repository.Service
	svc     *Service
	local   repository.Repository
	virtual repository.Repository
	clock   atomic.Int64
}

func newDebianFixture(t testing.TB) *debianFixture {
	t.Helper()

	ctx := t.Context()
	log := slog.New(slog.DiscardHandler)
	db := databasetest.NewDB(t)
	repos := repository.NewService(db, objectstoretest.NewMemory(), log)

	for _, r := range []repository.Repository{
		{
			Name:   "deb-local",
			Class:  repository.ClassLocal,
			Format: repository.FormatDebian,
		},
		{
			Name:   "deb",
			Class:  repository.ClassVirtual,
			Format: repository.FormatDebian,
			Config: repository.Config{
				Virtual: &repository.VirtualConfig{
					Members: []string{
						"deb-local",
					},
				},
			},
		},
	} {
		if err := repos.CreateRepository(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	f := &debianFixture{
		repos: repos,
		svc:   NewService(repos, nil, log),
	}

	var err error
	if f.local, err = repos.GetRepository(ctx, "deb-local"); err != nil {
		t.Fatal(err)
	}

	if f.virtual, err = repos.GetRepository(ctx, "deb"); err != nil {
		t.Fatal(err)
	}

	f.clock.Store(time.Now().Unix())
	f.svc.now = func() time.Time {
		return time.Unix(f.clock.Load(), 0)
	}

	return f
}

func (f *debianFixture) advance(d time.Duration) { f.clock.Add(int64(d / time.Second)) }

func (f *debianFixture) deploy(t testing.TB, name string, props repository.Properties) string {
	t.Helper()

	body := "deb " + name
	props[PropControl] = []string{
		"Package: " + name + "\nVersion: 1.0\nArchitecture: amd64\nDescription: " + strings.Repeat("x", 2000) + "\n",
	}
	itemPath := "pool/" + name + "_1.0_amd64.deb"

	if _, err := f.repos.Deploy(t.Context(), repository.DeployInput{
		Repository: f.local.Name,
		Path:       itemPath,
		Body:       strings.NewReader(body),
		Size:       int64(len(body)),
		User:       "test",
		Props:      props,
	}); err != nil {
		t.Fatal(err)
	}

	return itemPath
}

func (f *debianFixture) packages(t *testing.T, r repository.Repository, dist, file string) []string {
	t.Helper()

	d, err := f.svc.Distribution(t.Context(), r, dist)
	if err != nil {
		t.Fatalf("%s %s: %v", r.Name, dist, err)
	}

	paragraphs := Paragraphs(d.Files[file])
	names := make([]string, 0, len(paragraphs))

	for _, p := range paragraphs {
		names = append(names, Field(p, "Package"))
	}

	return names
}

func TestDistributionMemberErrors(t *testing.T) {
	t.Parallel()

	f := newDebianFixture(t)

	if _, err := f.svc.Distribution(t.Context(), f.local, "bookworm"); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("a distribution no member has = %v, want ErrNotFound", err)
	}

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := f.svc.Distribution(cancelled, f.local, "bookworm")
	if err == nil || errors.Is(err, repository.ErrNotFound) {
		t.Errorf("a distribution every member failed to read = %v, want the members' error", err)
	}
}

func TestDistributionServesTheBuiltCopyWhileItRebuilds(t *testing.T) {
	t.Parallel()

	f := newDebianFixture(t)
	bookworm := repository.Properties{
		PropDistribution: {
			"bookworm",
		},
	}
	f.deploy(t, "first", bookworm)

	const file = "main/binary-amd64/Packages"

	if got := f.packages(t, f.virtual, "bookworm", file); !slices.Equal(got, []string{
		"first",
	}) {
		t.Fatalf("the first build lists %v", got)
	}

	f.deploy(t, "second", bookworm)
	f.advance(31 * time.Second)

	if got := f.packages(t, f.virtual, "bookworm", file); !slices.Equal(got, []string{
		"first",
	}) {
		t.Errorf("an expired index = %v, want the built copy served while it rebuilds", got)
	}

	f.svc.indexes.Wait()

	if got := f.packages(t, f.virtual, "bookworm", file); !slices.Equal(got, []string{
		"first",
		"second",
	}) {
		t.Errorf("after the rebuild = %v, want both packages", got)
	}
}

func BenchmarkDistributionBurst(b *testing.B) {
	f := newDebianFixture(b)

	for i := range 300 {
		f.deploy(b, fmt.Sprintf("package%03d", i), repository.Properties{
			PropDistribution: {
				"bookworm",
			},
		})
	}

	const parallel = 8

	for b.Loop() {
		f.advance(time.Hour)

		var wg sync.WaitGroup
		for range parallel {
			wg.Go(func() {
				if _, err := f.svc.Distribution(b.Context(), f.virtual, "bookworm"); err != nil {
					b.Error(err)
				}
			})
		}

		wg.Wait()
	}
}
