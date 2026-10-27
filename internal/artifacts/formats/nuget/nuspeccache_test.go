package nuget

import (
	"strings"
	"testing"
)

func TestNuspecCacheKeepsTheMostRecentWithinItsCapacity(t *testing.T) {
	t.Parallel()

	entry := func(sha256 string, size int) cachedNuspec {
		return cachedNuspec{
			sha256: sha256,
			raw:    []byte(strings.Repeat("x", size)),
		}
	}

	cache := newNuspecCache(100)
	cache.add(entry("a", 20))
	cache.add(entry("b", 20))

	if _, ok := cache.get("a"); !ok {
		t.Fatal("a cached nuspec is missing")
	}

	cache.add(entry("c", 20))

	if _, ok := cache.get("b"); ok {
		t.Error("the least recently used nuspec stayed above the capacity")
	}

	for _, sha256 := range []string{
		"a",
		"c",
	} {
		if _, ok := cache.get(sha256); !ok {
			t.Errorf("nuspec %s was evicted, want it kept", sha256)
		}
	}

	cache.add(entry("huge", 60))

	if _, ok := cache.get("huge"); ok {
		t.Error("a nuspec larger than the whole cache was kept")
	}

	cache.add(entry("", 1))

	if len(cache.bySHA256) != 2 || cache.size != 80 {
		t.Errorf("cache holds %d entries, %d bytes; want 2, 80", len(cache.bySHA256), cache.size)
	}
}
