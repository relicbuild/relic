package blocking

import (
	"context"
	"errors"
	"fmt"
	"strings"

	packageurl "github.com/package-url/packageurl-go"

	"relic/internal/artifacts/repository"
	"relic/internal/scanning/malicious"
)

// VersionGate refuses the versions clients must not be offered: those the
// malicious package catalog lists, and cached ones the quarantine holds or a
// watch's rules block.
func (b *Blocker) VersionGate(catalog *malicious.Catalog, repos *repository.Service) repository.VersionGate {
	return func(
		ctx context.Context,
		r repository.Repository,
		ecosystem, name string,
		versions []repository.Version,
	) (map[string]bool, error) {
		refused := map[string]bool{}

		namespace, base := "", name
		if scope, rest, ok := strings.Cut(name, "/"); ok && strings.HasPrefix(scope, "@") {
			namespace, base = scope, rest
		}

		purls := make([]string, 0, len(versions))
		for _, v := range versions {
			purls = append(purls, packageurl.NewPackageURL(ecosystem, namespace, base, v.Version, nil, "").ToString())
		}

		if catalog != nil {
			matches, err := catalog.Match(ctx, purls)
			if err != nil {
				return nil, fmt.Errorf("check malicious packages: %w", err)
			}

			for _, m := range matches {
				refused[m.Version] = true
			}
		}

		for _, v := range versions {
			if refused[v.Version] || v.Path == "" {
				continue
			}

			it, err := repos.Stat(ctx, r.Name, v.Path)
			if errors.Is(err, repository.ErrNotFound) || (err == nil && it.IsFolder()) {
				continue
			}

			if err != nil {
				return nil, err
			}

			d, err := b.quarantine(ctx, r.Name, v.Path, it.Checksums.SHA256)
			if err == nil && !d.Blocked {
				d, err = b.decide(ctx, r.Name, v.Path, it.Checksums.SHA256)
			}

			if err != nil {
				return nil, err
			}

			refused[v.Version] = d.Blocked
		}

		return refused, nil
	}
}
