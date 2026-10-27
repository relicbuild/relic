package blocking

import (
	"context"
	"fmt"
	"sync"
	"time"

	"relic/internal/platform/database"
	"relic/internal/platform/telemetry"
	"relic/internal/scanning/scan"
)

const componentCacheLimit = 1 << 16

type cachedComponents struct {
	updated    time.Time
	components []scan.Component
}

type componentCache struct {
	mu      sync.Mutex
	entries map[string]cachedComponents
	held    int
}

func (c *componentCache) load(
	ctx context.Context,
	db *database.DB,
	sha256Hex string,
	updated time.Time,
) ([]scan.Component, error) {
	if comps, ok := c.lookup(sha256Hex, updated); ok {
		return comps, nil
	}

	row, err := db.GetScanSbom(ctx, sha256Hex)
	if err != nil {
		return nil, fmt.Errorf("load SBOM: %w", err)
	}

	var comps []scan.Component

	if row.Sbom != nil {
		parsed, err := scan.Components(row.Sbom)
		if err != nil {
			return nil, fmt.Errorf("read SBOM: %w", err)
		}

		comps = licenseFields(parsed)
	}

	c.store(sha256Hex, row.Updated, comps)

	return comps[:len(comps):len(comps)], nil
}

func (c *componentCache) lookup(sha256Hex string, updated time.Time) ([]scan.Component, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[sha256Hex]
	hit := ok && entry.updated.Equal(updated)

	telemetry.CacheLookup("components", hit)

	if !hit {
		return nil, false
	}

	return entry.components[:len(entry.components):len(entry.components)], true
}

func (c *componentCache) store(sha256Hex string, updated time.Time, comps []scan.Component) {
	if len(comps) > componentCacheLimit {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.entries == nil {
		c.entries = map[string]cachedComponents{}
	}

	if previous, ok := c.entries[sha256Hex]; ok {
		c.held -= len(previous.components)
		delete(c.entries, sha256Hex)
	}

	for key, entry := range c.entries {
		if c.held+len(comps) <= componentCacheLimit && len(c.entries) < componentCacheLimit {
			break
		}

		c.held -= len(entry.components)
		delete(c.entries, key)
	}

	c.entries[sha256Hex] = cachedComponents{
		updated:    updated,
		components: comps,
	}
	c.held += len(comps)
}

func licenseFields(comps []scan.Component) []scan.Component {
	out := make([]scan.Component, len(comps))
	for i, c := range comps {
		out[i] = scan.Component{
			Name:       c.Name,
			Version:    c.Version,
			PURL:       c.PURL,
			Licenses:   c.Licenses,
			LicenseIDs: c.LicenseIDs,
		}
	}

	return out
}
