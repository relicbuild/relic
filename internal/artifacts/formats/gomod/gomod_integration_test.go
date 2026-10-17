//go:build integration

package gomod_test

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"relic/internal/artifacts/formats/gomod"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
)

func TestMain(m *testing.M) { databasetest.Main(m) }

func TestUpstreamAndLocalErrors(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	log := slog.New(slog.DiscardHandler)
	repos := repository.NewService(databasetest.NewDB(t), objectstoretest.NewMemory(), log)
	repos.AllowPrivateUpstreams(true)

	svc := gomod.NewService(repos, log)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/example.com/m/@v/list":
		case "/example.com/m/@v/v1.0.0.info":
			_, _ = io.WriteString(w, "not json")
		case "/example.com/m/@latest":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	remote := repository.Repository{
		Name:   "go-remote",
		Class:  repository.ClassRemote,
		Format: gomod.Format,
		Config: repository.Config{
			Remote: &repository.RemoteConfig{
				URL: srv.URL,
			},
		},
	}
	local := repository.Repository{
		Name:   "go-local",
		Class:  repository.ClassLocal,
		Format: gomod.Format,
	}

	for _, r := range []repository.Repository{
		remote,
		local,
	} {
		if err := repos.CreateRepository(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := svc.Latest(ctx, remote, "example.com/m"); !errors.Is(err, repository.ErrUpstream) {
		t.Errorf("Latest with a failing upstream @latest = %v, want ErrUpstream", err)
	}

	if _, err := svc.Info(ctx, remote, "example.com/m", "v1.0.0"); !errors.Is(err, repository.ErrUpstream) {
		t.Errorf("Info of a malformed upstream .info = %v, want ErrUpstream", err)
	}

	if _, err := repos.Deploy(ctx, repository.DeployInput{
		Repository: "go-local",
		Path:       "example.com/m/@v/v1.0.0.info",
		Body:       strings.NewReader("not json"),
		User:       "admin",
	}); err != nil {
		t.Fatal(err)
	}

	_, err := svc.Info(ctx, local, "example.com/m", "v1.0.0")
	if err == nil || errors.Is(err, repository.ErrUpstream) {
		t.Errorf("Info of a malformed local .info = %v, want an error that is not ErrUpstream", err)
	}
}
