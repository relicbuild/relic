//go:build integration

package pypi_test

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"relic/internal/artifacts/formats/pypi"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
)

type coolingUpstream struct {
	jsonRequests atomic.Int32
	newRelease   atomic.Bool
}

func (u *coolingUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	old := time.Now().Add(-30 * 24 * time.Hour).UTC().Format(time.RFC3339)
	fresh := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)

	switch {
	case r.URL.Path == "/simple/demo/" && strings.Contains(r.Header.Get("Accept"), "json"):
		u.jsonRequests.Add(1)

		files := `{"filename":"demo-1.0.tar.gz","upload-time":"` + old + `"}`
		if u.newRelease.Load() {
			files += `,{"filename":"demo-1.1.tar.gz","upload-time":"` + fresh + `"}`
		}

		_, _ = io.WriteString(w, `{"files":[`+files+`]}`)
	case r.URL.Path == "/simple/demo/":
		page := `<a href="/packages/demo-1.0.tar.gz" data-requires-python="&gt;=3.9">demo-1.0.tar.gz</a>`
		if u.newRelease.Load() {
			page += `<a href="/packages/demo-1.1.tar.gz">demo-1.1.tar.gz</a>`
		}

		_, _ = io.WriteString(w, page)
	case strings.HasPrefix(r.URL.Path, "/packages/"):
		_, _ = io.WriteString(w, "sdist")
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func newCoolingRemote(t *testing.T) (*repository.Service, *pypi.Service, *coolingUpstream, repository.Repository) {
	t.Helper()

	repos := repository.NewService(databasetest.NewDB(t), objectstoretest.NewMemory(), slog.New(slog.DiscardHandler))
	repos.AllowPrivateUpstreams(true)
	svc := pypi.NewService(repos, slog.New(slog.DiscardHandler))

	upstream := &coolingUpstream{}
	srv := httptest.NewServer(upstream)
	t.Cleanup(srv.Close)

	r := repository.Repository{
		Name:   "pypi-remote",
		Class:  repository.ClassRemote,
		Format: pypi.Format,
		Config: repository.Config{
			Remote: &repository.RemoteConfig{
				URL:          srv.URL,
				IndexURL:     srv.URL,
				CooldownDays: 3,
			},
		},
	}
	if err := repos.CreateRepository(t.Context(), r); err != nil {
		t.Fatal(err)
	}

	return repos, svc, upstream, r
}

func TestLinksParseAnUnchangedPageAndFetchItsUploadTimesOnce(t *testing.T) {
	t.Parallel()

	_, svc, upstream, r := newCoolingRemote(t)

	for range 3 {
		links, err := svc.Links(t.Context(), r, "demo")
		if err != nil || len(links) != 1 || links[0].File != "demo-1.0.tar.gz" || links[0].RequiresPython != ">=3.9" {
			t.Fatalf("links = %+v, %v; want demo-1.0 requiring >=3.9", links, err)
		}
	}

	if n := upstream.jsonRequests.Load(); n != 1 {
		t.Errorf("3 requests of an unchanged page fetched the upload times %d times, want 1", n)
	}
}

func TestCooldownRefusesADistributionTheCachedPageDoesNotList(t *testing.T) {
	t.Parallel()

	repos, svc, upstream, r := newCoolingRemote(t)

	if _, err := svc.Links(t.Context(), r, "demo"); err != nil {
		t.Fatal(err)
	}

	upstream.newRelease.Store(true)

	if _, err := repos.Resolve(
		t.Context(),
		r.Name,
		"packages/demo-1.1.tar.gz",
	); !errors.Is(
		err,
		repository.ErrCoolingDown,
	) {
		t.Errorf("a cooling-down distribution newer than the cached page = %v, want ErrCoolingDown", err)
	}
}
