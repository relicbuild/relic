package npm

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"time"

	"golang.org/x/mod/semver"

	"relic/internal/artifacts/repository"
)

// checkTarball refuses a tarball whose version is cooling down, and finds its
// dist.shasum, both in its cached packument or else the upstream one.
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

	if index, ok := s.cachedIndex(ctx, r, name); ok {
		if sums, matched, err := s.tarballChecksums(index, file, cooldown); matched {
			return sums, err
		}
	}

	accept := AbbreviatedMediaType
	if cooldown > 0 {
		accept = "application/json"
	}

	upstream := strings.TrimSuffix(cfg.URL, "/")

	body, err := s.repos.FetchUpstream(ctx, r, upstream+"/"+escapeName(name), accept, maxPackumentBytes)
	if errors.Is(err, repository.ErrNotFound) {
		return repository.Checksums{}, nil
	}

	if err != nil {
		return repository.Checksums{}, err
	}

	index, err := readPackumentIndex(bytes.NewReader(body))
	if err != nil {
		return repository.Checksums{}, nil //nolint:nilerr // an unreadable packument checks nothing
	}

	sums, _, err := s.tarballChecksums(index, file, cooldown)

	return sums, err
}

func (s *Service) cachedIndex(ctx context.Context, r repository.Repository, name string) (packumentIndex, bool) {
	item, err := s.repos.Stat(ctx, r.Name, packumentCachePath(name))
	if err != nil || item.IsFolder() {
		return packumentIndex{}, false
	}

	index, err := s.packumentIndex(ctx, item)

	return index, err == nil
}

func (s *Service) tarballChecksums(
	index packumentIndex,
	file string,
	cooldown time.Duration,
) (repository.Checksums, bool, error) {
	matched := false

	for _, v := range index.versions {
		if !strings.HasSuffix(v.tarball, "/-/"+file) {
			continue
		}

		matched = true

		if published, ok := publishTime(index.times, v.version); ok && cooldown > 0 &&
			s.now().Sub(published) < cooldown {
			return repository.Checksums{}, true, repository.ErrCoolingDown
		}

		if len(v.shasum) == 40 {
			return repository.Checksums{
				SHA1: strings.ToLower(v.shasum),
			}, true, nil
		}
	}

	return repository.Checksums{}, matched, nil
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
