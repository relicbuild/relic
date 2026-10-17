//go:build integration

package npm_test

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"relic/internal/artifacts/formats/npm"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
)

func TestCooldownHidesAndRefusesYoungVersions(t *testing.T) {
	t.Parallel()

	db := databasetest.NewDB(t)
	repos := repository.NewService(db, objectstoretest.NewMemory(), slog.New(slog.DiscardHandler))
	repos.AllowPrivateUpstreams(true)
	svc := npm.NewService(db, repos, slog.New(slog.DiscardHandler))

	old := time.Now().Add(-30 * 24 * time.Hour).UTC().Format(time.RFC3339)
	fresh := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)

	var srv *httptest.Server

	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/left-pad":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"name":"left-pad","dist-tags":{"latest":"1.1.0","next":"1.1.0"},
			  "time":{"1.0.0":"`+old+`","1.1.0":"`+fresh+`"},
			  "versions":{
			    "1.0.0":{"dist":{"tarball":"`+srv.URL+`/left-pad/-/left-pad-1.0.0.tgz"}},
			    "1.1.0":{"dist":{"tarball":"`+srv.URL+`/left-pad/-/left-pad-1.1.0.tgz"}}}}`)
		case "/left-pad/-/left-pad-1.0.0.tgz", "/left-pad/-/left-pad-1.1.0.tgz":
			_, _ = io.WriteString(w, "tarball")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	if err := repos.CreateRepository(t.Context(), repository.Repository{
		Name:   "npmjs",
		Class:  repository.ClassRemote,
		Format: npm.Format,
		Config: repository.Config{
			Remote: &repository.RemoteConfig{
				URL:          srv.URL,
				CooldownDays: 7,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	doc, err := svc.Packument(t.Context(), "npmjs", "left-pad", "http://relic/npm")
	if err != nil {
		t.Fatal(err)
	}

	versions, _ := doc["versions"].(map[string]any)
	tags, _ := doc["dist-tags"].(map[string]any)

	if _, young := versions["1.1.0"]; young || len(versions) != 1 || tags["latest"] != "1.0.0" || tags["next"] != nil {
		t.Errorf("packument versions = %v, tags = %v; want only 1.0.0, tagged latest", versions, tags)
	}

	if _, err := repos.Resolve(
		t.Context(),
		"npmjs",
		"left-pad/-/left-pad-1.1.0.tgz",
	); !errors.Is(
		err,
		repository.ErrCoolingDown,
	) {
		t.Errorf("a cooling-down tarball = %v, want ErrCoolingDown", err)
	}

	if _, err := repos.Resolve(t.Context(), "npmjs", "left-pad/-/left-pad-1.0.0.tgz"); err != nil {
		t.Errorf("an old tarball = %v", err)
	}
}

func TestLocalOnlyScopeIsNotMergedFromRemotes(t *testing.T) {
	t.Parallel()

	db := databasetest.NewDB(t)
	repos := repository.NewService(db, objectstoretest.NewMemory(), slog.New(slog.DiscardHandler))
	repos.AllowPrivateUpstreams(true)
	svc := npm.NewService(db, repos, slog.New(slog.DiscardHandler))

	var srv *httptest.Server

	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"name":"@acme/ui","dist-tags":{"latest":"99.0.0"},
		  "versions":{"99.0.0":{"dist":{"tarball":"`+srv.URL+`/@acme/ui/-/ui-99.0.0.tgz"}}}}`)
	}))
	t.Cleanup(srv.Close)

	for _, r := range []repository.Repository{
		{
			Name:   "npm-local",
			Class:  repository.ClassLocal,
			Format: npm.Format,
		},
		{
			Name:   "npmjs",
			Class:  repository.ClassRemote,
			Format: npm.Format,
			Config: repository.Config{
				Remote: &repository.RemoteConfig{
					URL: srv.URL,
				},
			},
		},
		{
			Name:   "npm",
			Class:  repository.ClassVirtual,
			Format: npm.Format,
			Config: repository.Config{
				Virtual: &repository.VirtualConfig{
					Members: []string{
						"npm-local",
						"npmjs",
					},
					LocalOnlyPatterns: []string{
						"@acme/*",
					},
				},
			},
		},
	} {
		if err := repos.CreateRepository(t.Context(), r); err != nil {
			t.Fatal(err)
		}
	}

	if doc, err := svc.Packument(t.Context(), "npm", "@acme/ui", "http://relic/npm"); !errors.Is(err, npm.ErrNotFound) {
		t.Errorf("a local-only package only the remote has = %v, %v; want not found", doc, err)
	}

	if _, err := svc.Packument(t.Context(), "npm", "left-pad", "http://relic/npm"); err != nil {
		t.Errorf("another package through the remote = %v", err)
	}
}
