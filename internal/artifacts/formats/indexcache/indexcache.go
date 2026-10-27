// Package indexcache keeps the indexes format services generate: each index
// is built by one build at a time, and an expired copy is served while it is
// rebuilt.
package indexcache

import (
	"context"
	"sync"
	"time"

	"relic/internal/platform/memorylimit"
	"relic/internal/platform/telemetry"
)

// Settings bound a Cache.
type Settings struct {
	// Fresh is how long a built index is served without a rebuild.
	Fresh time.Duration
	// Stale is how long after its build an index is still served while a
	// rebuild runs; an older one makes callers wait for the rebuild.
	Stale time.Duration
	// Entries is the most indexes kept; the least recently used goes first.
	Entries int
	// Bytes is the most bytes the kept indexes take, as the cache's size
	// function counts them; the least recently used go first.
	Bytes int64
	// Builds is the most builds that run at once.
	Builds int
	// Timeout bounds one build.
	Timeout time.Duration
}

// DefaultSettings are the settings of the Debian and RPM caches.
var DefaultSettings = Settings{
	Fresh:   30 * time.Second,
	Stale:   10 * time.Minute,
	Entries: 64,
	Bytes:   256 << 20,
	Builds:  2,
	Timeout: 10 * time.Minute,
}

// Cache holds built indexes of type V by key.
type Cache[V any] struct {
	format   string
	settings Settings
	size     func(V) int64
	now      func() time.Time
	builds   chan struct{}

	mu      sync.Mutex
	entries map[string]*entry[V]
	bytes   int64
	running sync.WaitGroup
}

type entry[V any] struct {
	value  V
	size   int64
	built  time.Time
	used   time.Time
	ready  bool
	flight *flight[V]
}

type flight[V any] struct {
	done  chan struct{}
	value V
	err   error
}

// New returns a Cache for format's indexes; size counts an index's bytes and
// now is the clock.
func New[V any](format string, settings Settings, size func(V) int64, now func() time.Time) *Cache[V] {
	settings.Bytes = memorylimit.Share(settings.Bytes, 16)

	return &Cache[V]{
		format:   format,
		settings: settings,
		size:     size,
		now:      now,
		builds:   make(chan struct{}, max(settings.Builds, 1)),
		entries:  map[string]*entry[V]{},
	}
}

// Get returns key's index: the built copy while it is fresh, an expired copy
// that is not yet stale while one rebuild runs, else the result of a build.
func (c *Cache[V]) Get(ctx context.Context, key string, build func(context.Context) (V, error)) (V, error) {
	now := c.now()

	c.mu.Lock()

	e := c.entries[key]
	if e != nil && e.ready {
		e.used = now
		age := now.Sub(e.built)

		if age < c.settings.Fresh {
			c.mu.Unlock()
			telemetry.IndexCacheLookup(c.format, "hit")

			return e.value, nil
		}

		if age < c.settings.Stale {
			if e.flight == nil {
				c.start(ctx, key, e, build)
			}

			value := e.value

			c.mu.Unlock()
			telemetry.IndexCacheLookup(c.format, "stale")

			return value, nil
		}
	}

	if e == nil {
		e = &entry[V]{
			used: now,
		}
		c.entries[key] = e
	}

	if e.flight == nil {
		c.start(ctx, key, e, build)
	}

	f := e.flight

	c.mu.Unlock()
	telemetry.IndexCacheLookup(c.format, "miss")

	select {
	case <-f.done:
		return f.value, f.err
	case <-ctx.Done():
		var zero V

		return zero, ctx.Err()
	}
}

// Wait blocks until no build runs.
func (c *Cache[V]) Wait() { c.running.Wait() }

func (c *Cache[V]) start(ctx context.Context, key string, e *entry[V], build func(context.Context) (V, error)) {
	f := &flight[V]{
		done: make(chan struct{}),
	}
	e.flight = f

	c.running.Add(1)

	go func() {
		defer c.running.Done()

		f.value, f.err = c.build(context.WithoutCancel(ctx), build)

		c.mu.Lock()
		defer c.mu.Unlock()

		e.flight = nil

		switch {
		case f.err == nil:
			size := c.size(f.value)
			c.resize(size - e.size)
			e.value, e.size, e.built, e.ready = f.value, size, c.now(), true
		case !e.ready:
			c.remove(key)
		}

		c.evict(key)
		close(f.done)
	}()
}

func (c *Cache[V]) build(ctx context.Context, build func(context.Context) (V, error)) (V, error) {
	ctx, cancel := context.WithTimeout(ctx, c.settings.Timeout)
	defer cancel()

	select {
	case c.builds <- struct{}{}:
	case <-ctx.Done():
		var zero V

		return zero, ctx.Err()
	}

	defer func() {
		<-c.builds
	}()

	return build(ctx)
}

func (c *Cache[V]) evict(newest string) {
	for len(c.entries) > c.settings.Entries || c.bytes > c.settings.Bytes {
		oldest := ""

		for key, e := range c.entries {
			if e.flight != nil || key == newest {
				continue
			}

			if oldest == "" || e.used.Before(c.entries[oldest].used) {
				oldest = key
			}
		}

		if oldest == "" {
			return
		}

		c.remove(oldest)
	}
}

func (c *Cache[V]) remove(key string) {
	c.resize(-c.entries[key].size)
	delete(c.entries, key)
}

func (c *Cache[V]) resize(delta int64) {
	c.bytes += delta
	telemetry.IndexCacheResized(c.format, delta)
}
