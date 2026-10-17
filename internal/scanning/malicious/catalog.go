package malicious

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	packageurl "github.com/package-url/packageurl-go"
	"golang.org/x/mod/semver"

	"relic/internal/platform/database"
)

// Match is a package an advisory names as malicious.
type Match struct {
	ID      string // MAL-…
	Summary string
	PURL    string // the package URL matched
	Name    string
	Version string
}

// Catalog answers which packages are malicious.
type Catalog struct {
	db *database.DB
}

// NewCatalog returns a Catalog over the stored advisories.
func NewCatalog(db *database.DB) *Catalog {
	return &Catalog{
		db: db,
	}
}

type wanted struct {
	purl, ecosystem, key, name, version string
}

// Match returns the advisories naming the packages; package URLs it cannot
// read or of other ecosystems are skipped.
func (c *Catalog) Match(ctx context.Context, purls []string) ([]Match, error) {
	var (
		want              []wanted
		ecosystems, names []string
	)

	for _, p := range purls {
		u, err := packageurl.FromString(p)
		if err != nil || u.Version == "" || !slices.ContainsFunc(Ecosystems, func(e Ecosystem) bool {
			return e.PURL == u.Type
		}) {
			continue
		}

		name := u.Name
		if u.Namespace != "" {
			name = u.Namespace + "/" + u.Name
		}

		w := wanted{
			purl:      p,
			ecosystem: u.Type,
			key:       Key(u.Type, name),
			name:      name,
			version:   u.Version,
		}
		want = append(want, w)

		if !slices.Contains(ecosystems, w.ecosystem) {
			ecosystems = append(ecosystems, w.ecosystem)
		}

		names = append(names, w.key)
	}

	if len(want) == 0 {
		return nil, nil
	}

	rows, err := c.db.ListMaliciousPackages(ctx, database.ListMaliciousPackagesParams{
		Ecosystems: ecosystems,
		Names:      slices.Compact(slices.Sorted(slices.Values(names))),
	})
	if err != nil {
		return nil, fmt.Errorf("look up malicious packages: %w", err)
	}

	var out []Match

	for _, r := range rows {
		var ranges []Range
		if err := json.Unmarshal(r.Ranges, &ranges); err != nil {
			return nil, fmt.Errorf("read %s: %w", r.ID, err)
		}

		for _, w := range want {
			if w.ecosystem != r.Ecosystem || w.key != r.Name || !affects(r.AllVersions, r.Versions, ranges, w.version) {
				continue
			}

			out = append(out, Match{
				ID:      r.ID,
				Summary: r.Summary,
				PURL:    w.purl,
				Name:    w.name,
				Version: w.version,
			})
		}
	}

	return out, nil
}

func affects(all bool, versions []string, ranges []Range, version string) bool {
	if all || slices.Contains(versions, version) {
		return true
	}

	v := "v" + strings.TrimPrefix(version, "v")
	if !semver.IsValid(v) {
		return false
	}

	for _, r := range ranges {
		in := false

		for _, e := range r.Events {
			switch {
			case e.Introduced != "":
				in = e.Introduced == "0" || semver.Compare(v, "v"+strings.TrimPrefix(e.Introduced, "v")) >= 0
			case e.Fixed != "" && in:
				if semver.Compare(v, "v"+strings.TrimPrefix(e.Fixed, "v")) < 0 {
					return true
				}

				in = false
			case e.LastAffected != "" && in:
				if semver.Compare(v, "v"+strings.TrimPrefix(e.LastAffected, "v")) <= 0 {
					return true
				}

				in = false
			}
		}

		if in {
			return true
		}
	}

	return false
}
