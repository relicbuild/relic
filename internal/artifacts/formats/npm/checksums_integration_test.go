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
	"sync/atomic"
	"testing"
	"time"

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

func TestTarballChecksReadTheCachedPackument(t *testing.T) {
	t.Parallel()

	genuine := sha1.Sum([]byte("genuine")) //nolint:gosec // npm's published shasum
	old := time.Now().Add(-30 * 24 * time.Hour).UTC().Format(time.RFC3339)
	fresh := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)

	var (
		packuments atomic.Int64
		srv        *httptest.Server
	)

	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/left-pad":
			packuments.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(
				w,
				`{"name":"left-pad","time":{"1.0.0":"`+old+`","1.1.0":"`+old+`","1.2.0":"`+fresh+`"},
			  "versions":{
			  "1.0.0":{"dist":{"tarball":"`+srv.URL+`/left-pad/-/left-pad-1.0.0.tgz","shasum":"`+hex.EncodeToString(genuine[:])+`"}},
			  "1.1.0":{"dist":{"tarball":"`+srv.URL+`/left-pad/-/left-pad-1.1.0.tgz","shasum":"`+hex.EncodeToString(genuine[:])+`"}},
			  "1.2.0":{"dist":{"tarball":"`+srv.URL+`/left-pad/-/left-pad-1.2.0.tgz"}}}}`,
			)
		case "/left-pad/-/left-pad-1.0.0.tgz":
			_, _ = io.WriteString(w, "tampered")
		case "/left-pad/-/left-pad-1.1.0.tgz", "/left-pad/-/left-pad-1.2.0.tgz":
			_, _ = io.WriteString(w, "genuine")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	svc, repos := remoteService(t, srv.URL, 7)

	if _, err := svc.Document(t.Context(), "npmjs", "left-pad", "http://relic/npm/npmjs", npm.FormFull); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		tarball string
		want    error
	}{
		{
			name:    "a tarball that does not match its shasum",
			tarball: "left-pad/-/left-pad-1.0.0.tgz",
			want:    repository.ErrUpstream,
		},
		{
			name:    "a tarball that matches its shasum",
			tarball: "left-pad/-/left-pad-1.1.0.tgz",
		},
		{
			name:    "a tarball cooling down",
			tarball: "left-pad/-/left-pad-1.2.0.tgz",
			want:    repository.ErrCoolingDown,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := repos.Resolve(t.Context(), "npmjs", tt.tarball); !errors.Is(err, tt.want) {
				t.Errorf("Resolve(%s) = %v, want %v", tt.tarball, err, tt.want)
			}
		})
	}

	t.Cleanup(func() {
		if n := packuments.Load(); n != 1 {
			t.Errorf("the packument was fetched %d times, want once", n)
		}
	})
}
