package npm

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"time"

	"golang.org/x/mod/semver"

	"relic/internal/artifacts/repository"
)

// checkTarball refuses a tarball whose version is cooling down, and finds its
// dist.shasum, both in its packument.
func (s *Service) checkTarball(
	ctx context.Context,
	r repository.Repository,
	_, p string,
	sent repository.Checksums,
) (repository.Checksums, error) {
	name, file, ok := strings.Cut(p, "/-/")
	if !ok || !strings.HasSuffix(file, ".tgz") || !ValidName(name) {
		return repository.Checksums{}, nil
	}

	cfg := r.Upstream()

	cooldown := cfg.Cooldown()
	if cooldown == 0 && sent != (repository.Checksums{}) {
		return repository.Checksums{}, nil
	}

	accept := AbbreviatedMediaType
	if cooldown > 0 {
		accept = "application/json"
	}

	upstream := strings.TrimSuffix(cfg.URL, "/")

	body, err := s.repos.FetchUpstream(ctx, r, upstream+"/"+escapeName(name), accept, 256<<20)
	if errors.Is(err, repository.ErrNotFound) {
		return repository.Checksums{}, nil
	}

	if err != nil {
		return repository.Checksums{}, err
	}

	var doc struct {
		Time     map[string]string `json:"time"`
		Versions map[string]struct {
			Dist struct {
				Tarball string `json:"tarball"`
				Shasum  string `json:"shasum"`
			} `json:"dist"`
		} `json:"versions"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return repository.Checksums{}, nil //nolint:nilerr // an unreadable packument checks nothing
	}

	for version, v := range doc.Versions {
		if !strings.HasSuffix(v.Dist.Tarball, "/-/"+file) {
			continue
		}

		if published, ok := publishTime(doc.Time, version); ok && cooldown > 0 && s.now().Sub(published) < cooldown {
			return repository.Checksums{}, repository.ErrCoolingDown
		}

		if len(v.Dist.Shasum) == 40 {
			return repository.Checksums{
				SHA1: strings.ToLower(v.Dist.Shasum),
			}, nil
		}
	}

	return repository.Checksums{}, nil
}

func publishTime(times map[string]string, version string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, times[version])
	return t, err == nil
}

// dropVersions removes from kept the versions drop names, and points the
// latest tag, when it named one, at the newest version left (by publish
// time, else by semver); other tags that named one are removed.
func dropVersions(kept map[string]bool, times, tags map[string]string, drop func(version string) bool) {
	var (
		newest     string
		newestTime time.Time
	)

	for _, version := range slices.Sorted(maps.Keys(kept)) {
		if drop(version) {
			delete(kept, version)
			continue
		}

		published, ok := publishTime(times, version)

		switch {
		case ok && published.After(newestTime):
			newest, newestTime = version, published
		case !ok && newestTime.IsZero() && semver.Compare("v"+version, "v"+newest) > 0:
			newest = version
		}
	}

	for tag, version := range tags {
		if kept[version] {
			continue
		}

		if tag == "latest" && newest != "" {
			tags[tag] = newest
		} else {
			delete(tags, tag)
		}
	}
}
