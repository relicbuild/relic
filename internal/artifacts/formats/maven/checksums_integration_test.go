//go:build integration

package maven_test

import (
	"crypto/sha1" //nolint:gosec // Maven's published checksum
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"relic/internal/artifacts/formats/maven"
	"relic/internal/artifacts/repository"
)

func TestRemoteJarsMatchTheirPublishedSha1(t *testing.T) {
	t.Parallel()

	repos, _ := newMaven(t)
	repos.AllowPrivateUpstreams(true)

	sum := sha1.Sum([]byte("genuine")) //nolint:gosec // Maven's published checksum
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/g/a/1.0/a-1.0.jar.sha1", "/g/b/1.0/b-1.0.jar.sha1":
			_, _ = io.WriteString(w, hex.EncodeToString(sum[:])+"  a-1.0.jar\n")
		case "/g/a/1.0/a-1.0.jar":
			_, _ = io.WriteString(w, "tampered")
		case "/g/b/1.0/b-1.0.jar":
			_, _ = io.WriteString(w, "genuine")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	if err := repos.CreateRepository(t.Context(), repository.Repository{
		Name:   "central",
		Class:  repository.ClassRemote,
		Format: maven.Format,
		Config: repository.Config{
			Remote: &repository.RemoteConfig{
				URL: srv.URL,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := repos.Resolve(t.Context(), "central", "g/a/1.0/a-1.0.jar"); !errors.Is(err, repository.ErrUpstream) {
		t.Errorf("a jar that does not match its .sha1 = %v, want ErrUpstream", err)
	}

	if _, err := repos.Resolve(t.Context(), "central", "g/b/1.0/b-1.0.jar"); err != nil {
		t.Errorf("a jar that matches its .sha1 = %v", err)
	}
}
