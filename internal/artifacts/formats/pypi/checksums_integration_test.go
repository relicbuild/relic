//go:build integration

package pypi_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"relic/internal/artifacts/formats/pypi"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
)

func TestMain(m *testing.M) { databasetest.Main(m) }

func TestRemoteDistributionsMatchTheirSha256(t *testing.T) {
	t.Parallel()

	repos := repository.NewService(databasetest.NewDB(t), objectstoretest.NewMemory(), slog.New(slog.DiscardHandler))
	repos.AllowPrivateUpstreams(true)
	svc := pypi.NewService(repos, slog.New(slog.DiscardHandler))

	sum := sha256.Sum256([]byte("genuine"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/simple/demo/":
			_, _ = io.WriteString(w, `<html><body>
			<a href="/packages/demo-1.0.tar.gz#sha256=`+hex.EncodeToString(sum[:])+`">demo-1.0.tar.gz</a>
			<a href="/packages/demo-1.1.tar.gz#sha256=`+hex.EncodeToString(sum[:])+`">demo-1.1.tar.gz</a>
			</body></html>`)
		case "/simple/python-3parclient/":
			_, _ = io.WriteString(w, `<html><body>
			<a href="/packages/python-3parclient-4.2.12.tar.gz#sha256=`+hex.EncodeToString(sum[:])+`">python-3parclient-4.2.12.tar.gz</a>
			</body></html>`)
		case "/packages/demo-1.0.tar.gz", "/packages/python-3parclient-4.2.12.tar.gz":
			_, _ = io.WriteString(w, "tampered")
		case "/packages/demo-1.1.tar.gz":
			_, _ = io.WriteString(w, "genuine")
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
				URL:      srv.URL,
				IndexURL: srv.URL,
			},
		},
	}
	if err := repos.CreateRepository(t.Context(), r); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Links(t.Context(), r, "demo"); err != nil {
		t.Fatal(err)
	}

	if _, err := repos.Resolve(
		t.Context(),
		"pypi-remote",
		"packages/demo-1.0.tar.gz",
	); !errors.Is(
		err,
		repository.ErrUpstream,
	) {
		t.Errorf("a distribution that does not match its sha256 = %v, want ErrUpstream", err)
	}

	if _, err := repos.Resolve(t.Context(), "pypi-remote", "packages/demo-1.1.tar.gz"); err != nil {
		t.Errorf("a distribution that matches its sha256 = %v", err)
	}

	if _, err := svc.Links(t.Context(), r, "python-3parclient"); err != nil {
		t.Fatal(err)
	}

	if _, err := repos.Resolve(
		t.Context(),
		"pypi-remote",
		"packages/python-3parclient-4.2.12.tar.gz",
	); !errors.Is(
		err,
		repository.ErrUpstream,
	) {
		t.Errorf("a mismatched sdist whose name holds a hyphen and a digit = %v, want ErrUpstream", err)
	}
}
