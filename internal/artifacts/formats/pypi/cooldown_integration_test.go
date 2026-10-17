//go:build integration

package pypi_test

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"relic/internal/artifacts/formats/pypi"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
)

func TestCooldownHidesAndRefusesYoungDistributions(t *testing.T) {
	t.Parallel()

	repos := repository.NewService(databasetest.NewDB(t), objectstoretest.NewMemory(), slog.New(slog.DiscardHandler))
	repos.AllowPrivateUpstreams(true)
	svc := pypi.NewService(repos, slog.New(slog.DiscardHandler))

	old := time.Now().Add(-30 * 24 * time.Hour).UTC().Format(time.RFC3339)
	fresh := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/simple/demo/" && strings.Contains(r.Header.Get("Accept"), "json"):
			_, _ = io.WriteString(w, `{"files":[
			  {"filename":"demo-1.0.tar.gz","upload-time":"`+old+`"},
			  {"filename":"demo-1.1.tar.gz","upload-time":"`+fresh+`"}]}`)
		case r.URL.Path == "/simple/demo/":
			_, _ = io.WriteString(w, `<a href="/packages/demo-1.0.tar.gz">demo-1.0.tar.gz</a>
			<a href="/packages/demo-1.1.tar.gz">demo-1.1.tar.gz</a>`)
		case strings.HasPrefix(r.URL.Path, "/packages/"):
			_, _ = io.WriteString(w, "sdist")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
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

	links, err := svc.Links(t.Context(), r, "demo")
	if err != nil || len(links) != 1 || links[0].File != "demo-1.0.tar.gz" {
		t.Errorf("links = %+v, %v; want only demo-1.0", links, err)
	}

	if _, err := repos.Resolve(
		t.Context(),
		"pypi-remote",
		"packages/demo-1.1.tar.gz",
	); !errors.Is(
		err,
		repository.ErrCoolingDown,
	) {
		t.Errorf("a cooling-down distribution = %v, want ErrCoolingDown", err)
	}

	if _, err := repos.Resolve(t.Context(), "pypi-remote", "packages/demo-1.0.tar.gz"); err != nil {
		t.Errorf("an old distribution = %v", err)
	}
}
