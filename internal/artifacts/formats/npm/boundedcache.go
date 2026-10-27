package npm

import (
	"container/list"
	"context"
	"sync"

	"golang.org/x/sync/singleflight"

	"relic/internal/platform/telemetry"
)

type boundedCache[V any] struct {
	name    string
	limit   int64
	size    func(V) int64
	flights singleflight.Group

	mu      sync.Mutex
	used    int64
	recent  *list.List
	entries map[string]*list.Element
}

type cachedValue[V any] struct {
	key   string
	value V
	size  int64
}

func newBoundedCache[V any](name string, limit int64, size func(V) int64) *boundedCache[V] {
	return &boundedCache[V]{
		name:    name,
		limit:   limit,
		size:    size,
		recent:  list.New(),
		entries: map[string]*list.Element{},
	}
}

func (c *boundedCache[V]) get(ctx context.Context, key string, build func(context.Context) (V, error)) (V, error) {
	if value, ok := c.cached(key); ok {
		telemetry.PackumentCacheLookup(c.name, "hit")
		return value, nil
	}

	telemetry.PackumentCacheLookup(c.name, "miss")

	built := c.flights.DoChan(key, func() (any, error) {
		if value, ok := c.cached(key); ok {
			return value, nil
		}

		value, err := build(context.WithoutCancel(ctx))
		if err != nil {
			return value, err
		}

		c.store(key, value)

		return value, nil
	})

	select {
	case result := <-built:
		value, _ := result.Val.(V)
		return value, result.Err
	case <-ctx.Done():
		var zero V

		return zero, ctx.Err()
	}
}

func (c *boundedCache[V]) cached(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	element, ok := c.entries[key]
	if !ok {
		var zero V

		return zero, false
	}

	c.recent.MoveToFront(element)

	entry, _ := element.Value.(*cachedValue[V])

	return entry.value, true
}

func (c *boundedCache[V]) store(key string, value V) {
	size := c.size(value)
	if size > c.limit {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.entries[key]; ok {
		return
	}

	c.entries[key] = c.recent.PushFront(&cachedValue[V]{
		key:   key,
		value: value,
		size:  size,
	})
	c.used += size

	for c.used > c.limit {
		oldest := c.recent.Back()
		entry, _ := oldest.Value.(*cachedValue[V])

		c.recent.Remove(oldest)
		delete(c.entries, entry.key)
		c.used -= entry.size
	}

	telemetry.PackumentCacheSize(c.name, c.used)
}
