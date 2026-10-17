//go:build integration

package repository

import (
	"context"
	"crypto/sha1" //nolint:gosec // wire checksum
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func countingUpstream(t *testing.T, handle func(w http.ResponseWriter, n int32)) (*httptest.Server, *atomic.Int32) {
	t.Helper()

	var hits atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handle(w, hits.Add(1))
	}))
	t.Cleanup(srv.Close)

	return srv, &hits
}

func TestRemoteMissedRetrievals(t *testing.T) {
	t.Parallel()

	s, _, _ := newTestService(t)
	srv, hits := countingUpstream(t, func(w http.ResponseWriter, _ int32) {
		w.WriteHeader(http.StatusNotFound)
	})

	off := int64(0)

	createRepo(t, s, remoteRepo("absent-remote", RemoteConfig{
		URL: srv.URL,
	}))
	createRepo(t, s, remoteRepo("absent-off-remote", RemoteConfig{
		URL:                               srv.URL,
		MissedRetrievalCachePeriodSeconds: &off,
	}))

	now := time.Now()
	s.now = func() time.Time {
		return now
	}

	for range 3 {
		if _, err := s.Resolve(t.Context(), "absent-remote", "a/missing.jar"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("resolve = %v", err)
		}
	}

	if n := hits.Load(); n != 1 {
		t.Errorf("upstream asked %d times for a missing file, want once in the missed-retrieval period", n)
	}

	now = now.Add(31 * time.Minute)

	if _, err := s.Resolve(
		t.Context(),
		"absent-remote",
		"a/missing.jar",
	); !errors.Is(err, ErrNotFound) ||
		hits.Load() != 2 {
		t.Errorf("after the period, resolve = %v with %d upstream requests, want a second request", err, hits.Load())
	}

	hits.Store(0)

	for range 2 {
		_, _ = s.Resolve(t.Context(), "absent-off-remote", "a/missing.jar")
	}

	if n := hits.Load(); n != 2 {
		t.Errorf("with the period off, upstream asked %d times, want every time", n)
	}
}

func TestRemoteUpstreamFailuresAreNotRemembered(t *testing.T) {
	t.Parallel()

	s, _, _ := newTestService(t)
	srv, hits := countingUpstream(t, func(w http.ResponseWriter, n int32) {
		switch {
		case n == 1:
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
		case n <= 6:
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			_, _ = io.WriteString(w, "content")
		}
	})
	createRepo(t, s, remoteRepo("flaky-remote", RemoteConfig{
		URL:                 srv.URL,
		TimeoutMilliseconds: 5000,
	}))

	start := time.Now()

	if _, err := s.Resolve(t.Context(), "flaky-remote", "f.bin"); !errors.Is(err, ErrUpstream) {
		t.Fatalf("resolve through a failing upstream = %v, want ErrUpstream", err)
	}

	if hits.Load() != 3 || time.Since(start) < time.Second {
		t.Errorf("%d attempts in %s, want 3 that waited the Retry-After second", hits.Load(), time.Since(start))
	}

	if _, err := s.Resolve(t.Context(), "flaky-remote", "f.bin"); !errors.Is(err, ErrUpstream) {
		t.Fatalf("second resolve = %v", err)
	}

	it, err := s.Resolve(t.Context(), "flaky-remote", "f.bin")
	if err != nil || readItem(t, s, it) != "content" {
		t.Errorf("once the upstream recovers, resolve = %v, %v; a failure must not be remembered", it, err)
	}
}

func TestRemoteFormatChecksums(t *testing.T) {
	t.Parallel()

	s, _, _ := newTestService(t)

	var body atomic.Value

	body.Store("tampered")

	srv, _ := countingUpstream(t, func(w http.ResponseWriter, _ int32) {
		_, _ = io.WriteString(w, body.Load().(string))
	})

	r := remoteRepo("published-remote", RemoteConfig{
		URL: srv.URL,
	})
	r.Format = FormatGeneric
	createRepo(t, s, r)

	sum := sha1.Sum([]byte("genuine")) //nolint:gosec // wire checksum

	s.SetUpstreamCheck(FormatGeneric, func(context.Context, Repository, string, string, Checksums) (Checksums, error) {
		return Checksums{
			SHA1: hex.EncodeToString(sum[:]),
		}, nil
	})

	if _, err := s.Resolve(t.Context(), "published-remote", "lib.jar"); !errors.Is(err, ErrUpstream) {
		t.Fatalf("content that does not match the published checksum = %v, want ErrUpstream", err)
	}

	if _, err := s.Stat(t.Context(), "published-remote", "lib.jar"); !errors.Is(err, ErrNotFound) {
		t.Errorf("mismatched content was stored: %v", err)
	}

	body.Store("genuine")

	if it, err := s.Resolve(t.Context(), "published-remote", "lib.jar"); err != nil || readItem(t, s, it) != "genuine" {
		t.Errorf("matching content = %v, %v", it, err)
	}
}

func itemContent(ctx context.Context, s *Service, it Item) (string, error) {
	rc, err := s.Open(ctx, it, nil)
	if err != nil {
		return "", err
	}

	defer func() {
		_ = rc.Close()
	}()

	b, err := io.ReadAll(rc)

	return string(b), err
}

func TestRemoteFetchCoalescedAcrossReplicas(t *testing.T) {
	t.Parallel()

	first, blobs, db := newTestService(t)
	second := NewService(db, blobs, slog.New(slog.DiscardHandler))
	second.AllowPrivateUpstreams(true)

	release := make(chan struct{})
	srv, hits := countingUpstream(t, func(w http.ResponseWriter, _ int32) {
		<-release

		_, _ = io.WriteString(w, "shared")
	})
	createRepo(t, first, remoteRepo("replicated-remote", RemoteConfig{
		URL:                 srv.URL,
		TimeoutMilliseconds: 5000,
	}))

	const each = 10

	errs := make(chan error, 2*each)
	for _, s := range []*Service{
		first,
		second,
	} {
		for range each {
			go func() {
				it, err := s.Resolve(t.Context(), "replicated-remote", "big.tar")
				if err == nil {
					var content string

					content, err = itemContent(t.Context(), s, it)
					if err == nil && content != "shared" {
						err = errors.New("wrong content")
					}
				}

				errs <- err
			}()
		}
	}

	time.Sleep(300 * time.Millisecond)
	close(release)

	for range 2 * each {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}

	if n := hits.Load(); n != 1 {
		t.Errorf("two replicas made %d upstream requests, want 1", n)
	}
}
