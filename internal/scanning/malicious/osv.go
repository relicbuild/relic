// Package malicious keeps the advisories of malicious packages (OSV
// records with MAL- IDs, OpenSSF's malicious-packages data) and tells which
// packages they name.
package malicious

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Ecosystem is an OSV ecosystem Relic serves, with its package URL type.
type Ecosystem struct {
	OSV  string // the feed's name: npm, PyPI, …
	PURL string // the package URL type
}

// Ecosystems are the feeds read: the ecosystems of the formats Relic serves.
var Ecosystems = []Ecosystem{
	{
		OSV:  "npm",
		PURL: "npm",
	},
	{
		OSV:  "PyPI",
		PURL: "pypi",
	},
	{
		OSV:  "Maven",
		PURL: "maven",
	},
	{
		OSV:  "Go",
		PURL: "golang",
	},
	{
		OSV:  "NuGet",
		PURL: "nuget",
	},
	{
		OSV:  "crates.io",
		PURL: "cargo",
	},
	{
		OSV:  "RubyGems",
		PURL: "gem",
	},
}

type record struct {
	ID        string    `json:"id"`
	Summary   string    `json:"summary"`
	Details   string    `json:"details"`
	Modified  time.Time `json:"modified"`
	Withdrawn string    `json:"withdrawn"`
	Affected  []struct {
		Package struct {
			Ecosystem string `json:"ecosystem"`
			Name      string `json:"name"`
		} `json:"package"`
		Versions []string `json:"versions"`
		Ranges   []Range  `json:"ranges"`
	} `json:"affected"`
}

// Range is an OSV range: events in order, each introducing or fixing.
type Range struct {
	Type   string  `json:"type"`
	Events []Event `json:"events"`
}

// Event is one point of a Range.
type Event struct {
	Introduced   string `json:"introduced,omitempty"`
	Fixed        string `json:"fixed,omitempty"`
	LastAffected string `json:"last_affected,omitempty"`
}

// Package is one package an advisory names.
type Package struct {
	ID          string
	Ecosystem   string // package URL type
	Name        string // as Key names it
	AllVersions bool
	Versions    []string
	Ranges      []Range // SEMVER ranges only
	Summary     string
	Modified    time.Time
}

func parse(b []byte, eco Ecosystem) (pkgs []Package, id string, withdrawn bool, err error) {
	var r record
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, "", false, fmt.Errorf("read OSV record: %w", err)
	}

	if !strings.HasPrefix(r.ID, "MAL-") {
		return nil, r.ID, false, nil
	}

	if r.Withdrawn != "" {
		return nil, r.ID, true, nil
	}

	summary := r.Summary
	if summary == "" {
		summary, _, _ = strings.Cut(r.Details, "\n")
	}

	for _, a := range r.Affected {
		if a.Package.Ecosystem != eco.OSV || a.Package.Name == "" {
			continue
		}

		p := Package{
			ID:        r.ID,
			Ecosystem: eco.PURL,
			Name:      Key(eco.PURL, a.Package.Name),
			Versions:  a.Versions,
			Summary:   summary,
			Modified:  r.Modified,
		}

		for _, rg := range a.Ranges {
			if allVersions(rg) {
				p.AllVersions = true
			} else if rg.Type == "SEMVER" {
				p.Ranges = append(p.Ranges, rg)
			}
		}

		if len(a.Versions) == 0 && len(a.Ranges) == 0 {
			p.AllVersions = true
		}

		pkgs = append(pkgs, p)
	}

	return pkgs, r.ID, false, nil
}

func allVersions(r Range) bool {
	if len(r.Events) == 0 {
		return false
	}

	for i, e := range r.Events {
		if e.Fixed != "" || e.LastAffected != "" || (i == 0 && e.Introduced != "0") || (i > 0 && e.Introduced != "") {
			return false
		}
	}

	return true
}

// Key names a package the way the table stores it, from an ecosystem's
// package URL type and the OSV name ("group:artifact" for Maven) or the
// package URL's namespace and name joined by "/".
func Key(purlType, name string) string {
	switch purlType {
	case "maven":
		return strings.Replace(name, ":", "/", 1)
	case "pypi":
		n := strings.ToLower(strings.NewReplacer("_", "-", ".", "-").Replace(name))
		for strings.Contains(n, "--") {
			n = strings.ReplaceAll(n, "--", "-")
		}

		return n
	case "nuget":
		return strings.ToLower(name)
	}

	return name
}
