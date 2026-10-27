package nuget

import (
	"container/list"
	"sync"
)

const nuspecCacheBytes = 16 << 20

type cachedNuspec struct {
	sha256 string
	raw    []byte
	nuspec Nuspec
}

type nuspecCache struct {
	mu       sync.Mutex
	capacity int
	size     int
	recent   *list.List
	bySHA256 map[string]*list.Element
}

func newNuspecCache(capacity int) *nuspecCache {
	return &nuspecCache{
		capacity: capacity,
		recent:   list.New(),
		bySHA256: map[string]*list.Element{},
	}
}

func (c *nuspecCache) get(sha256 string) (cachedNuspec, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	e, ok := c.bySHA256[sha256]
	if !ok {
		return cachedNuspec{}, false
	}

	c.recent.MoveToFront(e)

	return e.Value.(cachedNuspec), true //nolint:forcetypeassert // the list holds cachedNuspec only
}

func (c *nuspecCache) add(entry cachedNuspec) {
	if entry.sha256 == "" || cachedSize(entry) > c.capacity {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.bySHA256[entry.sha256]; ok {
		return
	}

	c.bySHA256[entry.sha256] = c.recent.PushFront(entry)
	c.size += cachedSize(entry)

	for c.size > c.capacity {
		oldest := c.recent.Back()
		evicted := c.recent.Remove(oldest).(cachedNuspec) //nolint:forcetypeassert // the list holds cachedNuspec only
		delete(c.bySHA256, evicted.sha256)
		c.size -= cachedSize(evicted)
	}
}

func cachedSize(entry cachedNuspec) int {
	return 2 * len(entry.raw)
}
