//go:build integration

package helm

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"relic/internal/artifacts/repository"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
)

func TestMain(m *testing.M) { databasetest.Main(m) }

const upstreamIndex = `apiVersion: v1
entries:
  remote-chart:
  - name: remote-chart
    version: 1.0.0
    urls:
    - charts/remote-chart-1.0.0.tgz
`

type helmFixture struct {
	repos   *repository.Service
	svc     *Service
	virtual repository.Repository
	clock   atomic.Int64
}

func newHelmFixture(t *testing.T) *helmFixture {
	t.Helper()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/"+IndexFile {
			_, _ = w.Write([]byte(upstreamIndex))
			return
		}

		http.NotFound(w, r)
	}))
	t.Cleanup(upstream.Close)

	log := slog.New(slog.DiscardHandler)
	db := databasetest.NewDB(t)
	f := &helmFixture{
		repos: repository.NewService(db, objectstoretest.NewMemory(), log),
	}
	f.repos.AllowPrivateUpstreams(true)
	f.svc = NewService(f.repos, log)

	for _, r := range []repository.Repository{
		{
			Name:   "helm-local",
			Class:  repository.ClassLocal,
			Format: repository.FormatHelm,
		},
		{
			Name:   "helm-remote",
			Class:  repository.ClassRemote,
			Format: repository.FormatHelm,
			Config: repository.Config{
				Remote: &repository.RemoteConfig{
					URL: upstream.URL,
				},
			},
		},
		{
			Name:   "helm",
			Class:  repository.ClassVirtual,
			Format: repository.FormatHelm,
			Config: repository.Config{
				Virtual: &repository.VirtualConfig{
					Members: []string{
						"helm-local",
						"helm-remote",
					},
				},
			},
		},
	} {
		if err := f.repos.CreateRepository(t.Context(), r); err != nil {
			t.Fatal(err)
		}
	}

	var err error
	if f.virtual, err = f.repos.GetRepository(t.Context(), "helm"); err != nil {
		t.Fatal(err)
	}

	f.clock.Store(time.Now().Unix())
	f.svc.now = func() time.Time {
		return time.Unix(f.clock.Load(), 0)
	}

	return f
}

func (f *helmFixture) advance(d time.Duration) { f.clock.Add(int64(d / time.Second)) }

func (f *helmFixture) deploy(t *testing.T, name string) {
	t.Helper()

	var archive bytes.Buffer

	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	chart := "apiVersion: v2\nname: " + name + "\nversion: 1.0.0\n"

	if err := tw.WriteHeader(&tar.Header{
		Name: name + "/Chart.yaml",
		Mode: 0o644,
		Size: int64(len(chart)),
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := tw.Write([]byte(chart)); err != nil {
		t.Fatal(err)
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := f.repos.Deploy(t.Context(), repository.DeployInput{
		Repository: "helm-local",
		Path:       name + "-1.0.0.tgz",
		Body:       bytes.NewReader(archive.Bytes()),
		Size:       int64(archive.Len()),
		User:       "test",
	}); err != nil {
		t.Fatal(err)
	}
}

func (f *helmFixture) index(t *testing.T, visibility repository.Visibility) Index {
	t.Helper()

	ctx := t.Context()
	if visibility != nil {
		ctx = repository.WithVisibility(ctx, visibility)
	}

	body, err := f.svc.BuildIndex(ctx, f.virtual)
	if err != nil {
		t.Fatal(err)
	}

	var idx Index
	if err := yaml.Unmarshal(body, &idx); err != nil {
		t.Fatal(err)
	}

	return idx
}

func chartNames(idx Index) []string {
	names := make([]string, 0, len(idx.Entries))
	for name := range idx.Entries {
		names = append(names, name)
	}

	slices.Sort(names)

	return names
}

type hidingVisibility struct {
	repo, hidden string
}

func (v hidingVisibility) Visible(repoKey, path string) bool {
	return repoKey != v.repo || !strings.HasPrefix(path, v.hidden)
}

func (v hidingVisibility) Restricted(repoKey string) bool { return repoKey == v.repo }

func TestBuildIndexServesUnchangedMembersFromTheCache(t *testing.T) {
	t.Parallel()

	f := newHelmFixture(t)
	f.deploy(t, "first")

	unrestricted := hidingVisibility{
		repo: "another-repository",
	}

	built := f.index(t, unrestricted)
	if got := chartNames(built); !slices.Equal(got, []string{
		"first",
		"remote-chart",
	}) {
		t.Fatalf("the first build lists %v", got)
	}

	f.advance(time.Minute)

	if again := f.index(t, unrestricted); again.Generated != built.Generated {
		t.Errorf("a second request was generated at %s, want the cached build of %s", again.Generated, built.Generated)
	}

	f.deploy(t, "second")

	if got := chartNames(f.index(t, unrestricted)); !slices.Equal(got, []string{
		"first",
		"remote-chart",
		"second",
	}) {
		t.Errorf("after a deploy the index lists %v, want the new chart too", got)
	}
}

func TestBuildIndexHidesARestrictedMembersCharts(t *testing.T) {
	t.Parallel()

	f := newHelmFixture(t)
	f.deploy(t, "shown")
	f.deploy(t, "hidden")

	if got := chartNames(f.index(t, nil)); len(got) != 3 {
		t.Fatalf("an unfiltered build lists %v, want every chart", got)
	}

	for range 2 {
		if got := chartNames(f.index(t, hidingVisibility{
			repo:   "helm-local",
			hidden: "hidden",
		})); !slices.Equal(got, []string{
			"remote-chart",
			"shown",
		}) {
			t.Errorf("a caller who may not see hidden gets %v, want remote-chart and shown", got)
		}
	}
}

func TestBuildIndexListsChartsOnlyWhenAMemberChanges(t *testing.T) {
	t.Parallel()

	f := newHelmFixture(t)
	f.deploy(t, "first")
	f.deploy(t, "second")

	var lists atomic.Int64

	listCharts := f.svc.listCharts
	f.svc.listCharts = func(ctx context.Context, repoKey string) ([]repository.Item, error) {
		lists.Add(1)

		return listCharts(ctx, repoKey)
	}

	built := f.index(t, nil)
	if got := chartNames(built); !slices.Equal(got, []string{
		"first",
		"remote-chart",
		"second",
	}) {
		t.Fatalf("the first build lists %v", got)
	}

	listed := lists.Load()

	f.advance(time.Minute)

	if again := f.index(t, nil); again.Generated != built.Generated {
		t.Errorf("a second request was generated at %s, want the cached build of %s", again.Generated, built.Generated)
	}

	if got := lists.Load(); got != listed {
		t.Errorf("a request for an unchanged index listed the charts %d more times", got-listed)
	}

	if err := f.repos.Delete(t.Context(), "helm-local", "first-1.0.0.tgz"); err != nil {
		t.Fatal(err)
	}

	if got := chartNames(f.index(t, nil)); !slices.Equal(got, []string{
		"remote-chart",
		"second",
	}) {
		t.Errorf("after a delete the index lists %v, want the deleted chart gone", got)
	}

	f.deploy(t, "third")

	if got := chartNames(f.index(t, nil)); !slices.Equal(got, []string{
		"remote-chart",
		"second",
		"third",
	}) {
		t.Errorf("after a deploy the index lists %v, want the new chart too", got)
	}
}
