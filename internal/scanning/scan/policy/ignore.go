package policy

import (
	"slices"
	"strings"
	"time"

	"relic/internal/scanning/scan"

	packageurl "github.com/package-url/packageurl-go"
)

// IgnoreRule ignores a violation when every non-empty filter matches it.
type IgnoreRule struct {
	ID      string     `json:"id,omitempty"`
	Notes   string     `json:"notes,omitempty"`
	Expires *time.Time `json:"expires,omitempty"`
	// Vulnerabilities are vulnerability IDs, CVEs included.
	Vulnerabilities []string `json:"vulnerabilities,omitempty"`
	CVEs            []string `json:"cves,omitempty"`
	Licenses        []string `json:"licenses,omitempty"`
	Watches         []string `json:"watches,omitempty"`
	Policies        []string `json:"policies,omitempty"`
	// Packages are package URLs; one without a version matches them all.
	Packages []string `json:"packages,omitempty"`
	// ArtifactPaths match "<repository>/<path>" prefixes, a whole path
	// segment at a time.
	ArtifactPaths []string `json:"artifactPaths,omitempty"`
	Builds        []Build  `json:"builds,omitempty"`
}

// Build names a build, and optionally one run of it.
type Build struct {
	Name   string `json:"name"`
	Number string `json:"number,omitempty"`
}

// Subject is what a violation is about, for ignore-rule matching.
type Subject struct {
	VulnerabilityIDs []string // the finding's vulnerability IDs (GHSA-…, an adapter's own ID)
	CVEs             []string
	License          string
	Watch            string
	Policy           string
	Packages         []string // package URLs
	ArtifactPath     string   // "repo/path/to/file"
	BuildName        string
	BuildNumber      string
}

// Empty reports whether the rule has no filters.
func (r IgnoreRule) Empty() bool {
	return len(r.Vulnerabilities)+len(r.CVEs)+len(r.Licenses)+len(r.Watches)+len(r.Policies)+
		len(r.Packages)+len(r.ArtifactPaths)+len(r.Builds) == 0
}

// Ignores reports whether the rule, unexpired at now, ignores s.
func (r IgnoreRule) Ignores(s Subject, now time.Time) bool {
	if r.Empty() || (r.Expires != nil && !now.Before(*r.Expires)) {
		return false
	}

	anyOf := func(filter, values []string) bool {
		return len(filter) == 0 || slices.ContainsFunc(values, func(v string) bool {
			return slices.ContainsFunc(filter, func(f string) bool {
				return strings.EqualFold(f, v)
			})
		})
	}
	if !anyOf(r.Vulnerabilities, append(slices.Clone(s.VulnerabilityIDs), s.CVEs...)) || !anyOf(r.CVEs, s.CVEs) ||
		!anyOf(r.Licenses, subjectLicenses(s.License)) || !anyOf(r.Watches, []string{
		s.Watch,
	}) ||
		!anyOf(r.Policies, []string{
			s.Policy,
		}) {
		return false
	}

	if len(r.Packages) > 0 && !slices.ContainsFunc(r.Packages, func(p string) bool {
		return slices.ContainsFunc(s.Packages, func(q string) bool {
			return PackageMatches(p, q)
		})
	}) {
		return false
	}

	if len(r.ArtifactPaths) > 0 && !slices.ContainsFunc(r.ArtifactPaths, func(p string) bool {
		return s.ArtifactPath != "" && p != "" && pathWithin(s.ArtifactPath, strings.TrimPrefix(p, "/"))
	}) {
		return false
	}

	if len(r.Builds) > 0 && !slices.ContainsFunc(r.Builds, func(b Build) bool {
		return b.Name == s.BuildName && (b.Number == "" || b.Number == s.BuildNumber)
	}) {
		return false
	}

	return true
}

func pathWithin(artifactPath, prefix string) bool {
	return prefix == "" || artifactPath == prefix ||
		strings.HasPrefix(artifactPath, strings.TrimSuffix(prefix, "/")+"/")
}

// PackageMatches reports whether a package URL filter matches a package:
// the same type, namespace and name, and the same version unless the
// filter has none.
func PackageMatches(filter, pkg string) bool {
	f, err := packageurl.FromString(filter)
	if err != nil {
		return false
	}

	p, err := packageurl.FromString(pkg)
	if err != nil {
		return false
	}

	return f.Type == p.Type && strings.EqualFold(f.Namespace, p.Namespace) && strings.EqualFold(f.Name, p.Name) &&
		(f.Version == "" || f.Version == p.Version)
}

// ValidPackage reports whether s is a package URL.
func ValidPackage(s string) bool {
	_, err := packageurl.FromString(s)
	return err == nil
}

func subjectLicenses(expr string) []string {
	if expr == "" {
		return []string{
			"",
		}
	}

	return append([]string{
		expr,
	}, scan.LicenseIDs(expr)...)
}
