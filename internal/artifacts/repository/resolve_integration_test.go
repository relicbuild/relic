//go:build integration

package repository

import (
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // wire checksum
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"relic/internal/platform/sealing"
)

func upstream(t *testing.T, files map[string]string) (*httptest.Server, *atomic.Int32) {
	t.Helper()

	var hits atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)

		if u, p, _ := r.BasicAuth(); u != "user" || p != "pw" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		switch body, ok := files[strings.TrimPrefix(r.URL.Path, "/libs/")]; {
		case r.URL.Path == "/libs/broken":
			w.WriteHeader(http.StatusInternalServerError)
		case r.URL.Path == "/libs/refused":
			w.WriteHeader(http.StatusForbidden)
		case r.URL.Path == "/libs/lying":
			w.Header().Set("X-Checksum-Sha1", strings.Repeat("0", 40))
			_, _ = io.WriteString(w, "content")
		case ok:
			sum := sha1.Sum([]byte(body)) //nolint:gosec // wire checksum
			w.Header().Set("X-Checksum-Sha1", hex.EncodeToString(sum[:]))
			_, _ = io.WriteString(w, body)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	return srv, &hits
}

func createRepo(t *testing.T, s *Service, r Repository) {
	t.Helper()

	if err := s.CreateRepository(t.Context(), r); err != nil {
		t.Fatal(err)
	}
}

func remoteRepo(key string, c RemoteConfig) Repository {
	return Repository{
		Name:  key,
		Class: ClassRemote,
		Config: Config{
			Remote: &c,
		},
	}
}

func virtualRepo(key, deployTo string, members ...string) Repository {
	return Repository{
		Name:  key,
		Class: ClassVirtual,
		Config: Config{
			Virtual: &VirtualConfig{
				Members:  members,
				DeployTo: deployTo,
			},
		},
	}
}

func readItem(t *testing.T, s *Service, it Item) string {
	t.Helper()

	content, err := itemContent(t.Context(), s, it)
	if err != nil {
		t.Fatal(err)
	}

	return content
}

func TestRemoteFetchThrough(t *testing.T) {
	t.Parallel()
	s, _, _ := newTestService(t)
	srv, hits := upstream(t, map[string]string{
		"a/lib.jar": "jar-bytes",
	})
	createRepo(
		t,
		s,
		remoteRepo(
			"libs-remote",
			RemoteConfig{
				URL:                 srv.URL + "/libs",
				Username:            "user",
				Password:            "pw",
				TimeoutMilliseconds: 5000,
			},
		),
	)

	it, err := s.Resolve(t.Context(), "libs-remote", "a/lib.jar")
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}

	if it.Repository != "libs-remote" || readItem(t, s, it) != "jar-bytes" {
		t.Errorf("cached item = %+v", it)
	}

	if _, err := s.Resolve(t.Context(), "libs-remote", "a/lib.jar"); err != nil {
		t.Fatal(err)
	}

	if n := hits.Load(); n != 1 {
		t.Errorf("upstream hits = %d, want 1 (second read served from cache)", n)
	}

	tests := []struct {
		name string
		path string
		want error
	}{
		{
			name: "missing upstream",
			path: "nope.jar",
			want: ErrNotFound,
		},
		{
			name: "upstream error",
			path: "broken",
			want: ErrUpstream,
		},
		{
			name: "upstream refuses (crates.io, rubygems.org answer 403 for a missing version)",
			path: "refused",
			want: ErrNotFound,
		},
		{
			name: "checksum mismatch is a broken upstream, never stored",
			path: "lying",
			want: ErrUpstream,
		},
		{
			name: "traversal",
			path: "../etc/passwd",
			want: ErrInvalidPath,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := s.Resolve(t.Context(), "libs-remote", tt.path); !errors.Is(err, tt.want) {
				t.Errorf("Resolve(%s) = %v, want %v", tt.path, err, tt.want)
			}
		})
	}

	if _, err := s.Deploy(
		t.Context(),
		DeployInput{
			Repository: "libs-remote",
			Path:       "x",
			Body:       strings.NewReader("x"),
		},
	); !errors.Is(err, ErrNotLocal) {
		t.Errorf("client deploy to remote = %v, want ErrNotLocal", err)
	}
}

func TestRemoteOfflineAndSecrets(t *testing.T) {
	t.Parallel()
	s, _, _ := newTestService(t)
	srv, hits := upstream(t, map[string]string{
		"f": "x",
	})
	createRepo(
		t,
		s,
		remoteRepo("off-remote", RemoteConfig{
			URL:      srv.URL + "/libs",
			Username: "user",
			Password: "pw",
			Offline:  true,
		}),
	)

	if _, err := s.Resolve(t.Context(), "off-remote", "f"); !errors.Is(err, ErrNotFound) || hits.Load() != 0 {
		t.Errorf("offline remote: err %v, hits %d", err, hits.Load())
	}

	if err := s.UpdateRepository(
		t.Context(),
		remoteRepo("off-remote", RemoteConfig{
			URL:      srv.URL + "/libs",
			Username: "user",
		}),
	); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Resolve(t.Context(), "off-remote", "f"); err != nil {
		t.Fatalf("after going online: %v", err)
	}

	if got, _ := s.GetRepository(t.Context(), "off-remote"); got.Upstream().Password != "pw" {
		t.Errorf("password after an update without one = %q, want the stored one", got.Upstream().Password)
	}

	right, wrong := sealingBox(t), sealingBox(t)
	s.SetSecrets(right)

	if err := s.UpdateRepository(t.Context(), remoteRepo("off-remote", RemoteConfig{
		URL:      srv.URL + "/libs",
		Username: "user",
		Password: "pw",
	})); err != nil {
		t.Fatal(err)
	}

	s.SetSecrets(wrong)

	if err := s.UpdateRepository(t.Context(), remoteRepo("off-remote", RemoteConfig{
		URL:      srv.URL + "/libs",
		Username: "user",
	})); err != nil {
		t.Fatal(err)
	}

	s.SetSecrets(right)

	if got, _ := s.GetRepository(t.Context(), "off-remote"); got.Upstream().Password != "pw" {
		t.Errorf("password after an update while the key did not open it = %q, want it kept", got.Upstream().Password)
	}
}

func sealingBox(t *testing.T) *sealing.Box {
	t.Helper()

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}

	box, err := sealing.New(key)
	if err != nil {
		t.Fatal(err)
	}

	return box
}

func TestVirtualResolution(t *testing.T) {
	t.Parallel()
	s, _, _ := newTestService(t)
	srv, _ := upstream(t, map[string]string{
		"shared.txt":      "from-remote",
		"only-remote.txt": "remote",
	})
	createRepo(t, s, Repository{
		Name:  "first-local",
		Class: ClassLocal,
	})
	createRepo(t, s, remoteRepo("up-remote", RemoteConfig{
		URL:      srv.URL + "/libs",
		Username: "user",
		Password: "pw",
	}))
	createRepo(t, s, virtualRepo("all-virtual", "first-local", "first-local", "up-remote"))
	createRepo(t, s, virtualRepo("loop-a", ""))
	createRepo(t, s, virtualRepo("loop-b", "", "loop-a"))

	if err := s.UpdateRepository(t.Context(), virtualRepo("loop-a", "", "loop-b")); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Deploy(
		t.Context(),
		DeployInput{
			Repository: "first-local",
			Path:       "shared.txt",
			Body:       strings.NewReader("from-local"),
		},
	); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name, repo, path, wantRepo, wantBody string
		wantErr                              error
	}{
		{
			name:     "first member wins",
			repo:     "all-virtual",
			path:     "shared.txt",
			wantRepo: "first-local",
			wantBody: "from-local",
		},
		{
			name:     "falls through to remote",
			repo:     "all-virtual",
			path:     "only-remote.txt",
			wantRepo: "up-remote",
			wantBody: "remote",
		},
		{
			name:    "nowhere",
			repo:    "all-virtual",
			path:    "absent.txt",
			wantErr: ErrNotFound,
		},
		{
			name:    "cycle terminates",
			repo:    "loop-a",
			path:    "x",
			wantErr: ErrNotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			it, err := s.Resolve(t.Context(), tt.repo, tt.path)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}

				return
			}

			if err != nil || it.Repository != tt.wantRepo || readItem(t, s, it) != tt.wantBody {
				t.Errorf("Resolve = %+v, %v", it, err)
			}
		})
	}

	if target, err := s.DeployTarget(t.Context(), "all-virtual"); err != nil || target != "first-local" {
		t.Errorf("DeployTarget = %q, %v", target, err)
	}

	if _, err := s.DeployTarget(t.Context(), "loop-a"); !errors.Is(err, ErrNotLocal) {
		t.Errorf("DeployTarget without default = %v, want ErrNotLocal", err)
	}
}

func TestRemoteFetchCoalesced(t *testing.T) {
	t.Parallel()

	s, _, _ := newTestService(t)

	var hits atomic.Int32

	arrived, release := make(chan struct{}, 64), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)

		arrived <- struct{}{}

		<-release

		_, _ = io.WriteString(w, "layer-bytes")
	}))
	t.Cleanup(srv.Close)
	createRepo(t, s, remoteRepo("herd-remote", RemoteConfig{
		URL:                 srv.URL,
		TimeoutMilliseconds: 5000,
	}))

	const clients = 50

	errs := make(chan error, clients)
	for range clients {
		go func() {
			it, err := s.Resolve(t.Context(), "herd-remote", "big/layer.tar")
			if err != nil {
				errs <- err
				return
			}

			content, err := itemContent(t.Context(), s, it)
			if err == nil && content != "layer-bytes" {
				err = errors.New("wrong content")
			}

			errs <- err
		}()
	}

	<-arrived
	time.Sleep(300 * time.Millisecond)
	close(release)

	for range clients {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}

	if n := hits.Load(); n != 1 {
		t.Fatalf("upstream requests = %d, want 1", n)
	}
}

func TestRemoteFetchRetriesLostConnection(t *testing.T) {
	t.Parallel()

	s, _, _ := newTestService(t)

	var hits atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) == 1 {
			conn, _, err := http.NewResponseController(w).Hijack()
			if err == nil {
				_ = conn.Close()
			}

			return
		}

		_, _ = io.WriteString(w, "after-retry")
	}))
	t.Cleanup(srv.Close)
	createRepo(t, s, remoteRepo("flaky-remote", RemoteConfig{
		URL:                 srv.URL,
		TimeoutMilliseconds: 5000,
	}))

	it, err := s.Resolve(t.Context(), "flaky-remote", "lib/file.jar")
	if err != nil {
		t.Fatal(err)
	}

	if got := readItem(t, s, it); got != "after-retry" || hits.Load() != 2 {
		t.Errorf("content %q after %d requests", got, hits.Load())
	}
}

func flakyUpstream(t *testing.T, failures int32, fail func(w http.ResponseWriter)) (*httptest.Server, *atomic.Int32) {
	t.Helper()

	var hits atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) <= failures {
			fail(w)

			return
		}

		_, _ = io.WriteString(w, "module-zip")
	}))
	t.Cleanup(srv.Close)

	return srv, &hits
}

func cutMidBody(w http.ResponseWriter) {
	w.Header().Set("Content-Length", "100")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "partial")

	conn, _, err := http.NewResponseController(w).Hijack()
	if err == nil {
		_ = conn.Close()
	}
}

func TestRemoteFetchRetriesTransientUpstreamFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		failures int32
		fail     func(w http.ResponseWriter)
		want     error
		hits     int32
	}{
		{
			name:     "connection cut mid-body, then served",
			failures: 2,
			fail:     cutMidBody,
			hits:     3,
		},
		{
			name:     "503, then served",
			failures: 1,
			fail: func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusServiceUnavailable)
			},
			hits: 2,
		},
		{
			name:     "cut every time is an upstream error, not a server error",
			failures: 100,
			fail:     cutMidBody,
			want:     ErrUpstream,
			hits:     3,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, _, _ := newTestService(t)
			srv, hits := flakyUpstream(t, tt.failures, tt.fail)
			createRepo(t, s, remoteRepo("go-remote", RemoteConfig{
				URL:                 srv.URL,
				TimeoutMilliseconds: 5000,
			}))

			it, err := s.Resolve(t.Context(), "go-remote", "m/@v/v1.0.0.zip")
			if !errors.Is(err, tt.want) || (tt.want == nil && err != nil) {
				t.Fatalf("Resolve = %v, want %v", err, tt.want)
			}

			if tt.want == nil && readItem(t, s, it) != "module-zip" {
				t.Error("cached content differs from the upstream's")
			}

			if n := hits.Load(); n != tt.hits {
				t.Errorf("upstream hits = %d, want %d", n, tt.hits)
			}
		})
	}
}

func TestRemoteFetchStallsAndSlowDownloads(t *testing.T) {
	t.Parallel()

	body := strings.Repeat("x", 40)

	tests := []struct {
		name  string
		serve func(w http.ResponseWriter, hit int32)
		hits  int32
	}{
		{
			name: "a body that stalls is abandoned after the timeout and fetched again",
			serve: func(w http.ResponseWriter, hit int32) {
				w.Header().Set("Content-Length", "40")
				_, _ = io.WriteString(w, body[:10])
				w.(http.Flusher).Flush()

				if hit == 1 {
					time.Sleep(3 * time.Second)

					return
				}

				_, _ = io.WriteString(w, body[10:])
			},
			hits: 2,
		},
		{
			name: "a slow body that keeps arriving is not cut off",
			serve: func(w http.ResponseWriter, _ int32) {
				w.Header().Set("Content-Length", "40")

				for i := range 40 {
					_, _ = io.WriteString(w, body[i:i+1])
					w.(http.Flusher).Flush()
					time.Sleep(60 * time.Millisecond)
				}
			},
			hits: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var hits atomic.Int32

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				tt.serve(w, hits.Add(1))
			}))
			t.Cleanup(srv.Close)

			s, _, _ := newTestService(t)
			createRepo(t, s, remoteRepo("slow-remote", RemoteConfig{
				URL:                 srv.URL,
				TimeoutMilliseconds: 500,
			}))

			it, err := s.Resolve(t.Context(), "slow-remote", "big.jar")
			if err != nil {
				t.Fatalf("Resolve = %v", err)
			}

			if readItem(t, s, it) != body {
				t.Error("cached content differs from the upstream's")
			}

			if n := hits.Load(); n != tt.hits {
				t.Errorf("upstream hits = %d, want %d", n, tt.hits)
			}
		})
	}
}

func TestRemoteThatDisablesTheCacheKeepsNothing(t *testing.T) {
	t.Parallel()
	s, _, _ := newTestService(t)
	srv, hits := upstream(t, map[string]string{
		"a/lib.jar": "jar-bytes",
	})
	createRepo(
		t,
		s,
		remoteRepo(
			"libs-nocache",
			RemoteConfig{
				URL:                 srv.URL + "/libs",
				Username:            "user",
				Password:            "pw",
				TimeoutMilliseconds: 5000,
				DisableCache:        true,
			},
		),
	)

	for range 2 {
		it, err := s.Resolve(t.Context(), "libs-nocache", "a/lib.jar")
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}

		if readItem(t, s, it) != "jar-bytes" {
			t.Errorf("served %+v, want the upstream's bytes", it)
		}
	}

	if n := hits.Load(); n != 2 {
		t.Errorf("upstream hits = %d, want 2 (nothing served from a cache)", n)
	}

	for _, path := range []string{
		"a/lib.jar",
		"a",
	} {
		if it, err := s.Stat(t.Context(), "libs-nocache", path); !errors.Is(err, ErrNotFound) {
			t.Errorf("Stat(%s) after the fetches = %+v, %v; want ErrNotFound", path, it.Name, err)
		}
	}

	if _, err := s.Resolve(t.Context(), "libs-nocache", "nope.jar"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Resolve of a missing file = %v, want ErrNotFound", err)
	}
}
