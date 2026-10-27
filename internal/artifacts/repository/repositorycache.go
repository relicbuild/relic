package repository

import (
	"maps"
	"slices"
	"sync"
	"time"

	"relic/internal/platform/telemetry"
)

const (
	repositoryCacheSize = 1024
	repositoryCacheTTL  = 5 * time.Second
)

type cachedRepository struct {
	repository Repository
	loaded     time.Time
}

type repositoryCache struct {
	mu         sync.Mutex
	entries    map[string]cachedRepository
	generation uint64
}

func (c *repositoryCache) lookup(key string) (Repository, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[key]
	hit := ok && time.Since(entry.loaded) < repositoryCacheTTL

	telemetry.CacheLookup("repository", hit)

	if !hit {
		return Repository{}, false
	}

	return entry.repository.clone(), true
}

func (c *repositoryCache) currentGeneration() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.generation
}

func (c *repositoryCache) store(r Repository, generation uint64, loaded time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if generation != c.generation {
		return
	}

	if c.entries == nil {
		c.entries = map[string]cachedRepository{}
	}

	if _, ok := c.entries[r.Name]; !ok && len(c.entries) >= repositoryCacheSize {
		for key := range c.entries {
			delete(c.entries, key)

			break
		}
	}

	c.entries[r.Name] = cachedRepository{
		repository: r.clone(),
		loaded:     loaded,
	}
}

func (c *repositoryCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.generation++
	clear(c.entries)
}

func (r Repository) clone() Repository {
	r.Config.Environments = slices.Clone(r.Config.Environments)
	r.Config.Architectures = slices.Clone(r.Config.Architectures)

	if r.Config.Remote != nil {
		remote := *r.Config.Remote
		if remote.MissedRetrievalCachePeriodSeconds != nil {
			period := *remote.MissedRetrievalCachePeriodSeconds
			remote.MissedRetrievalCachePeriodSeconds = &period
		}

		r.Config.Remote = &remote
	}

	if r.Config.Virtual != nil {
		virtual := *r.Config.Virtual
		virtual.Members = slices.Clone(virtual.Members)
		virtual.LocalOnlyPatterns = slices.Clone(virtual.LocalOnlyPatterns)
		r.Config.Virtual = &virtual
	}

	if r.Config.Extensions != nil {
		extensions := maps.Clone(r.Config.Extensions)
		for key, value := range extensions {
			extensions[key] = slices.Clone(value)
		}

		r.Config.Extensions = extensions
	}

	return r
}
