package indexcache

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

var testSettings = Settings{
	Fresh:   30 * time.Second,
	Stale:   10 * time.Minute,
	Entries: 8,
	Builds:  4,
	Timeout: time.Hour,
}

type counted struct {
	builds  atomic.Int32
	running atomic.Int32
	most    atomic.Int32
	release chan struct{}
	fail    atomic.Bool
}

func newCounted() *counted {
	return &counted{
		release: make(chan struct{}),
	}
}

func (c *counted) build(ctx context.Context) (int, error) {
	n := c.builds.Add(1)

	running := c.running.Add(1)
	defer c.running.Add(-1)

	for {
		most := c.most.Load()
		if running <= most || c.most.CompareAndSwap(most, running) {
			break
		}
	}

	select {
	case <-c.release:
	case <-ctx.Done():
		return 0, ctx.Err()
	}

	if c.fail.Load() {
		return 0, errors.New("upstream unavailable")
	}

	return int(n), nil
}

func getAll(
	cache *Cache[int],
	ctx context.Context,
	key string,
	callers int,
	build func(context.Context) (int, error),
) []int {
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		got []int
	)

	for range callers {
		wg.Go(func() {
			v, err := cache.Get(ctx, key, build)
			if err != nil {
				v = -1
			}

			mu.Lock()

			got = append(got, v)
			mu.Unlock()
		})
	}

	wg.Wait()

	return got
}

func TestCacheGet(t *testing.T) {
	t.Parallel()

	t.Run("concurrent misses share one build", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			cache := New[int]("debian", testSettings, time.Now)
			b := newCounted()

			done := make(chan []int)
			go func() {
				done <- getAll(cache, t.Context(), "bookworm", 8, b.build)
			}()

			synctest.Wait()

			if n := b.builds.Load(); n != 1 {
				t.Errorf("8 callers of a missing index ran %d builds, want 1", n)
			}

			close(b.release)

			for _, v := range <-done {
				if v != 1 {
					t.Errorf("a caller got %d, want the one build's 1", v)
				}
			}
		})
	})

	t.Run("a fresh index is served without a build", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			cache := New[int]("debian", testSettings, time.Now)
			b := newCounted()
			close(b.release)

			_, _ = cache.Get(t.Context(), "bookworm", b.build)

			time.Sleep(testSettings.Fresh - time.Second)

			if v, _ := cache.Get(t.Context(), "bookworm", b.build); v != 1 || b.builds.Load() != 1 {
				t.Errorf("a fresh index = %d after %d builds, want 1 after 1", v, b.builds.Load())
			}
		})
	})

	t.Run("an expired index is served while one rebuild runs", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			cache := New[int]("debian", testSettings, time.Now)
			b := newCounted()

			first := make(chan int)

			go func() {
				v, _ := cache.Get(t.Context(), "bookworm", b.build)
				first <- v
			}()

			synctest.Wait()

			b.release <- struct{}{}

			<-first

			time.Sleep(testSettings.Fresh + time.Second)

			for _, v := range getAll(cache, t.Context(), "bookworm", 8, b.build) {
				if v != 1 {
					t.Errorf("a caller of an expired index got %d, want the built copy 1 at once", v)
				}
			}

			synctest.Wait()

			if n := b.builds.Load(); n != 2 {
				t.Errorf("8 callers of an expired index started %d rebuilds, want 1", n-1)
			}

			b.release <- struct{}{}

			cache.Wait()

			if v, _ := cache.Get(t.Context(), "bookworm", b.build); v != 2 {
				t.Errorf("after the rebuild = %d, want 2", v)
			}
		})
	})

	t.Run("an index past its stale time waits for the rebuild", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			cache := New[int]("debian", testSettings, time.Now)
			b := newCounted()
			close(b.release)

			_, _ = cache.Get(t.Context(), "bookworm", b.build)

			time.Sleep(testSettings.Stale)

			if v, _ := cache.Get(t.Context(), "bookworm", b.build); v != 2 {
				t.Errorf("an index past its stale time = %d, want the rebuilt 2", v)
			}
		})
	})

	t.Run("a caller that leaves does not cancel the build", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			cache := New[int]("debian", testSettings, time.Now)
			b := newCounted()

			leaving, leave := context.WithCancel(t.Context())

			left := make(chan error)

			go func() {
				_, err := cache.Get(leaving, "bookworm", b.build)
				left <- err
			}()

			stayed := make(chan int)

			go func() {
				v, _ := cache.Get(t.Context(), "bookworm", b.build)
				stayed <- v
			}()

			synctest.Wait()
			leave()

			if err := <-left; !errors.Is(err, context.Canceled) {
				t.Errorf("the caller that left = %v, want context.Canceled", err)
			}

			close(b.release)

			if v := <-stayed; v != 1 {
				t.Errorf("the caller that stayed = %d, want 1", v)
			}
		})
	})

	t.Run("a failed build is not kept and a failed rebuild keeps the copy", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			cache := New[int]("debian", testSettings, time.Now)
			b := newCounted()
			close(b.release)
			b.fail.Store(true)

			if _, err := cache.Get(t.Context(), "bookworm", b.build); err == nil {
				t.Fatal("a failed build returned no error")
			}

			b.fail.Store(false)

			if v, err := cache.Get(t.Context(), "bookworm", b.build); err != nil || v != 2 {
				t.Fatalf("after a failed build = %d, %v, want a new build", v, err)
			}

			time.Sleep(testSettings.Fresh)
			b.fail.Store(true)

			_, _ = cache.Get(t.Context(), "bookworm", b.build)
			cache.Wait()

			if v, err := cache.Get(t.Context(), "bookworm", b.build); err != nil || v != 2 {
				t.Errorf("after a failed rebuild = %d, %v, want the built copy 2", v, err)
			}

			cache.Wait()
		})
	})

	t.Run("the least recently used index goes first", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			settings := testSettings
			settings.Entries = 2
			cache := New[int]("debian", settings, time.Now)
			b := newCounted()
			close(b.release)

			for _, key := range []string{
				"bookworm",
				"trixie",
				"bookworm",
				"noble",
			} {
				_, _ = cache.Get(t.Context(), key, b.build)

				time.Sleep(time.Second)
			}

			if len(cache.entries) != 2 || cache.entries["trixie"] != nil {
				t.Errorf("kept %d indexes, trixie kept: %v; want 2 without trixie",
					len(cache.entries), cache.entries["trixie"] != nil)
			}
		})
	})

	t.Run("builds of different indexes are bounded", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			settings := testSettings
			settings.Builds = 2
			cache := New[int]("debian", settings, time.Now)
			b := newCounted()

			var wg sync.WaitGroup
			for i := range 6 {
				wg.Go(func() {
					_, _ = cache.Get(t.Context(), fmt.Sprint("dist", i), b.build)
				})
			}

			synctest.Wait()

			if n := b.running.Load(); n != 2 {
				t.Errorf("%d builds run at once, want 2", n)
			}

			close(b.release)
			wg.Wait()

			if most := b.most.Load(); most > 2 {
				t.Errorf("up to %d builds ran at once, want at most 2", most)
			}
		})
	})
}
