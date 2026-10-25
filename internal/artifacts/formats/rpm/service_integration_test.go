//go:build integration

package rpm

import (
	"bytes"
	"fmt"
	"log/slog"
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

type rpmFixture struct {
	repos *repository.Service
	svc   *Service
	clock atomic.Int64
}

func newRPMFixture(t testing.TB, repos ...repository.Repository) *rpmFixture {
	t.Helper()

	log := slog.New(slog.DiscardHandler)
	f := &rpmFixture{
		repos: repository.NewService(databasetest.NewDB(t), objectstoretest.NewMemory(), log),
	}
	f.repos.AllowPrivateUpstreams(true)
	f.svc = NewService(f.repos, nil, log)

	for _, r := range repos {
		if err := f.repos.CreateRepository(t.Context(), r); err != nil {
			t.Fatal(err)
		}
	}

	f.clock.Store(time.Now().Unix())
	f.svc.now = func() time.Time {
		return time.Unix(f.clock.Load(), 0)
	}

	return f
}

func (f *rpmFixture) repository(t testing.TB, name string) repository.Repository {
	t.Helper()

	r, err := f.repos.GetRepository(t.Context(), name)
	if err != nil {
		t.Fatal(err)
	}

	return r
}

func (f *rpmFixture) deploy(t testing.TB, repo, folder, name string) {
	t.Helper()

	raw := buildRPM(t, testPackage{
		name:    name,
		version: "1.0",
		release: "1",
		arch:    "noarch",
		files: []string{
			"/usr/share/doc/" + name + "/README",
		},
	})

	if _, err := f.repos.Deploy(t.Context(), repository.DeployInput{
		Repository: repo,
		Path:       strings.TrimPrefix(folder+"/"+name+"-1.0-1.noarch.rpm", "/"),
		Body:       bytes.NewReader(raw),
		Size:       int64(len(raw)),
		User:       "test",
	}); err != nil {
		t.Fatal(err)
	}
}

func primaryOf(t *testing.T, files Repodata) map[string]string {
	t.Helper()

	primary := readRepodata(t, files)["primary"]
	if primary.Count != len(primary.Packages) {
		t.Errorf("primary says it has %d packages and lists %d", primary.Count, len(primary.Packages))
	}

	locations := map[string]string{}
	for _, p := range primary.Packages {
		locations[p.Name] = p.Location.Href
	}

	return locations
}

func virtualOf(name string, depth int32, members ...string) repository.Repository {
	return repository.Repository{
		Name:   name,
		Class:  repository.ClassVirtual,
		Format: repository.FormatRPM,
		Config: repository.Config{
			MetadataDepth: depth,
			Virtual: &repository.VirtualConfig{
				Members: members,
			},
		},
	}
}

func localOf(name string, depth int32) repository.Repository {
	return repository.Repository{
		Name:   name,
		Class:  repository.ClassLocal,
		Format: repository.FormatRPM,
		Config: repository.Config{
			MetadataDepth: depth,
		},
	}
}

func TestRepodataServesTheBuiltCopyWhileItRebuilds(t *testing.T) {
	t.Parallel()

	f := newRPMFixture(t, localOf("rpm-local", 0), virtualOf("rpm", 0, "rpm-local"))
	virtual := f.repository(t, "rpm")

	f.deploy(t, "rpm-local", "", "first")

	files, err := f.svc.Repodata(t.Context(), virtual, "")
	if err != nil {
		t.Fatal(err)
	}

	if got := primaryOf(t, files); len(got) != 1 {
		t.Fatalf("the first build lists %v", got)
	}

	f.deploy(t, "rpm-local", "", "second")
	f.clock.Add(31)

	if files, err = f.svc.Repodata(t.Context(), virtual, ""); err != nil {
		t.Fatal(err)
	}

	if got := primaryOf(t, files); len(got) != 1 {
		t.Errorf("expired repodata = %v, want the built copy served while it rebuilds", got)
	}

	f.svc.indexes.Wait()

	if files, err = f.svc.Repodata(t.Context(), virtual, ""); err != nil {
		t.Fatal(err)
	}

	if got := primaryOf(t, files); len(got) != 2 {
		t.Errorf("after the rebuild = %v, want both packages", got)
	}
}

func TestRepodataOfAVirtualFollowsItsMetadataDepth(t *testing.T) {
	t.Parallel()

	f := newRPMFixture(t, localOf("rpm-local", 1), virtualOf("rpm", 1, "rpm-local"))
	virtual := f.repository(t, "rpm")

	f.deploy(t, "rpm-local", "f43", "older")
	f.deploy(t, "rpm-local", "f44", "newer")

	if ServesRepodata(virtual, "") || !ServesRepodata(virtual, "f43") {
		t.Errorf("a virtual repository of metadataDepth 1 serves repodata at its root: %v, in f43: %v",
			ServesRepodata(virtual, ""), ServesRepodata(virtual, "f43"))
	}

	files, err := f.svc.Repodata(t.Context(), virtual, "f43")
	if err != nil {
		t.Fatal(err)
	}

	if got := primaryOf(t, files); len(got) != 1 || got["older"] != "older-1.0-1.noarch.rpm" {
		t.Errorf("f43 of the virtual lists %v, want only older at older-1.0-1.noarch.rpm", got)
	}
}

func BenchmarkRepodataBurst(b *testing.B) {
	f := newRPMFixture(b, localOf("rpm-local", 0), virtualOf("rpm", 0, "rpm-local"))

	for i := range 100 {
		f.deploy(b, "rpm-local", "", fmt.Sprintf("package%03d", i))
	}

	virtual := f.repository(b, "rpm")

	const parallel = 8

	for b.Loop() {
		f.clock.Add(int64(time.Hour / time.Second))

		var wg sync.WaitGroup
		for range parallel {
			wg.Go(func() {
				if _, err := f.svc.Repodata(b.Context(), virtual, ""); err != nil {
					b.Error(err)
				}
			})
		}

		wg.Wait()
	}
}
