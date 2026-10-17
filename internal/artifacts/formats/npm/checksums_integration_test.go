//go:build integration

package npm_test

import (
	"crypto/sha1" //nolint:gosec // npm's published shasum
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"relic/internal/artifacts/formats/npm"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
)

func TestMain(m *testing.M) { databasetest.Main(m) }

func TestRemoteTarballsMatchTheirShasum(t *testing.T) {
	t.Parallel()

	db := databasetest.NewDB(t)
	repos := repository.NewService(db, objectstoretest.NewMemory(), slog.New(slog.DiscardHandler))
	repos.AllowPrivateUpstreams(true)
	npm.NewService(db, repos, slog.New(slog.DiscardHandler))

	sum := sha1.Sum([]byte("genuine")) //nolint:gosec // npm's published shasum

	var srv *httptest.Server

	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/left-pad":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"name":"left-pad","versions":{
			  "1.0.0":{"dist":{"tarball":"`+srv.URL+`/left-pad/-/left-pad-1.0.0.tgz","shasum":"`+hex.EncodeToString(sum[:])+`"}},
			  "1.1.0":{"dist":{"tarball":"`+srv.URL+`/left-pad/-/left-pad-1.1.0.tgz","shasum":"`+hex.EncodeToString(sum[:])+`"}}}}`)
		case "/left-pad/-/left-pad-1.0.0.tgz":
			_, _ = io.WriteString(w, "tampered")
		case "/left-pad/-/left-pad-1.1.0.tgz":
			_, _ = io.WriteString(w, "genuine")
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
				URL: srv.URL,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := repos.Resolve(
		t.Context(),
		"npmjs",
		"left-pad/-/left-pad-1.0.0.tgz",
	); !errors.Is(
		err,
		repository.ErrUpstream,
	) {
		t.Errorf("a tarball that does not match its shasum = %v, want ErrUpstream", err)
	}

	if _, err := repos.Resolve(t.Context(), "npmjs", "left-pad/-/left-pad-1.1.0.tgz"); err != nil {
		t.Errorf("a tarball that matches its shasum = %v", err)
	}
}
