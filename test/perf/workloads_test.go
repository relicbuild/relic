//go:build perf

package perf

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"
)

type workload struct {
	name string
	run  func(ctx context.Context, t *testing.T, s server) []result
}

var workloads = []workload{
	{
		name: "startup",
		run:  startup,
	},
	{
		name: "load",
		run:  load,
	},
	{
		name: "large",
		run:  large,
	},
	{
		name: "npm",
		run:  npm,
	},
	{
		name: "pypi",
		run:  pypi,
	},
	{
		name: "maven",
		run:  maven,
	},
	{
		name: "debian",
		run:  debian,
	},
	{
		name: "delete",
		run:  deleteFolder,
	},
	{
		name: "idle",
		run:  idle,
	},
}

func pause(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func startup(ctx context.Context, _ *testing.T, _ server) []result {
	pause(ctx, *startupFlag)

	return nil
}

func idle(ctx context.Context, _ *testing.T, _ server) []result {
	pause(ctx, *idleFlag)

	return nil
}

func concurrently(ctx context.Context, s server, count int, paths ...string) []result {
	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		results []result
	)

	for _, path := range paths {
		for range count {
			wg.Go(func() {
				r := s.fetch(ctx, http.MethodGet, path, nil)

				mu.Lock()

				results = append(results, r)
				mu.Unlock()
			})
		}
	}

	wg.Wait()

	return results
}

type unique struct {
	size int
}

func (u unique) reader() io.Reader {
	return io.LimitReader(rand.Reader, int64(u.size))
}

func load(ctx context.Context, t *testing.T, s server) []result {
	const (
		repository  = "load-local"
		seeds       = 30
		uploaders   = 15
		downloaders = 35
	)

	s.createRepository(ctx, t, repository, "FORMAT_GENERIC", "")

	for i := range seeds {
		r := s.fetch(ctx, http.MethodPut, fmt.Sprintf("/api/v1/files/%s/seed/seed-%d.dat", repository, i),
			unique{
				size: 50 << 10,
			}.reader())
		if r.failed() {
			t.Fatalf("seed: %v", r)
		}
	}

	ctx, cancel := context.WithTimeout(ctx, *loadFlag)
	defer cancel()

	var (
		mu        sync.Mutex
		wg        sync.WaitGroup
		uploads   []result
		downloads []result
	)

	record := func(into *[]result, r result) {
		if ctx.Err() != nil {
			return
		}

		mu.Lock()

		*into = append(*into, r)
		mu.Unlock()
	}

	for worker := range uploaders {
		wg.Go(func() {
			for i := 0; ctx.Err() == nil; i++ {
				size := 50 << 10

				switch i % 10 {
				case 0:
					size = 2 << 20
				case 1, 2, 3:
					size = 500 << 10
				}

				path := fmt.Sprintf("/api/v1/files/%s/load/load-%d-%d.dat", repository, worker, i)
				record(&uploads, s.fetch(ctx, http.MethodPut, path, unique{
					size: size,
				}.reader()))
				pause(ctx, 100*time.Millisecond)
			}
		})
	}

	for worker := range downloaders {
		wg.Go(func() {
			for i := 0; ctx.Err() == nil; i++ {
				path := fmt.Sprintf("/api/v1/files/%s/seed/seed-%d.dat", repository, (worker+i)%seeds)
				record(&downloads, s.fetch(ctx, http.MethodGet, path, nil))
				pause(ctx, 50*time.Millisecond)
			}
		})
	}

	wg.Wait()

	t.Logf("load: %d uploads p95 %v, %d downloads p95 %v",
		len(uploads), percentile(uploads, 0.95), len(downloads), percentile(downloads, 0.95))

	return slices.Concat(uploads, downloads)
}

func percentile(results []result, p float64) time.Duration {
	if len(results) == 0 {
		return 0
	}

	durations := make([]time.Duration, 0, len(results))
	for _, r := range results {
		durations = append(durations, r.duration)
	}

	slices.Sort(durations)

	return durations[int(math.Ceil(p*float64(len(durations))))-1]
}

func large(ctx context.Context, t *testing.T, s server) []result {
	const path = "/api/v1/files/large-local/large.bin"

	s.createRepository(ctx, t, "large-local", "FORMAT_GENERIC", "")

	upload := s.fetch(ctx, http.MethodPut, path, unique{
		size: 1 << 30,
	}.reader())

	return append([]result{
		upload,
	}, concurrently(ctx, s, 2, path)...)
}

func npm(ctx context.Context, t *testing.T, s server) []result {
	s.createRepository(ctx, t, "npm-remote", "FORMAT_NPM", "https://registry.npmjs.org")

	names := []string{
		"typescript",
		"@types%2fnode",
		"aws-sdk",
		"next",
		"react-native",
	}

	var results []result

	for range 2 {
		for _, name := range names {
			results = append(results, s.fetch(ctx, http.MethodGet, "/npm/npm-remote/"+name, nil))
		}
	}

	return append(results, concurrently(ctx, s, 10, "/npm/npm-remote/next")...)
}

func pypi(ctx context.Context, t *testing.T, s server) []result {
	s.createRepository(ctx, t, "pypi-remote", "FORMAT_PYPI", "https://pypi.org")

	results := make([]result, 0, 30)

	for range 2 {
		results = append(results, concurrently(ctx, s, 5,
			"/pypi/pypi-remote/simple/boto3/",
			"/pypi/pypi-remote/simple/botocore/",
			"/pypi/pypi-remote/simple/numpy/",
		)...)
	}

	return results
}

func maven(ctx context.Context, t *testing.T, s server) []result {
	s.createRepository(ctx, t, "maven-remote", "FORMAT_MAVEN", "https://repo1.maven.org/maven2")

	return concurrently(ctx, s, 5,
		"/maven/maven-remote/org/springframework/spring-core/6.1.14/spring-core-6.1.14.jar",
		"/maven/maven-remote/com/google/guava/guava/33.3.1-jre/guava-33.3.1-jre.jar",
		"/maven/maven-remote/software/amazon/awssdk/bom/maven-metadata.xml",
	)
}

func debian(ctx context.Context, t *testing.T, s server) []result {
	s.createRepository(ctx, t, "debian-remote", "FORMAT_DEBIAN", "https://deb.debian.org/debian")

	var results []result

	for range 2 {
		results = append(results, concurrently(ctx, s, 2,
			"/debian/debian-remote/dists/bookworm/InRelease",
			"/debian/debian-remote/dists/trixie/InRelease",
			"/debian/debian-remote/dists/bookworm-updates/InRelease",
			"/debian/debian-remote/dists/trixie-updates/InRelease",
		)...)
	}

	return append(
		results,
		concurrently(ctx, s, 2, "/debian/debian-remote/dists/bookworm/main/binary-amd64/Packages.xz")...)
}

func deleteFolder(ctx context.Context, t *testing.T, s server) []result {
	r := s.fetch(ctx, http.MethodDelete, "/api/v1/repositories/load-local/items/load", nil)
	t.Logf("delete: %v", r)

	if r.status == http.StatusNotFound {
		t.Log("delete: no load folder; run the load workload first")

		return nil
	}

	return []result{
		r,
	}
}
