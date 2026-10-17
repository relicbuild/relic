package scan

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/github/go-spdx/v2/spdxexp"
)

// Component is one package of a stored SBOM, as the UI lists it.
type Component struct {
	Name     string   `json:"name"`
	Version  string   `json:"version"`
	Type     string   `json:"type"`
	PURL     string   `json:"purl,omitempty"`
	Licenses []string `json:"licenses"`
	// LicenseIDs are the licenses the Licenses expressions name, each once.
	LicenseIDs []string   `json:"licenseIds"`
	Locations  []Location `json:"locations"`
}

// Location is where a component was found: a path, and for images the
// layer holding it.
type Location struct {
	Path    string `json:"path"`
	LayerID string `json:"layerId,omitempty"`
}

// Components lists the packages of a stored (gzipped) SBOM, sorted by name
// and version.
func Components(stored []byte) ([]Component, error) {
	raw, err := gunzip(stored)
	if err != nil {
		return nil, fmt.Errorf("read stored SBOM: %w", err)
	}

	out, err := sbomComponents(raw)
	if err != nil {
		return nil, fmt.Errorf("parse stored SBOM: %w", err)
	}

	slices.SortFunc(out, func(a, b Component) int {
		return cmp.Or(strings.Compare(a.Name, b.Name), strings.Compare(a.Version, b.Version))
	})

	return out, nil
}

// newComponent is a component with licenses from expressions or names.
func newComponent(name, version, typ, purl string, licenses []string, locations []Location) Component {
	c := Component{
		Name:       name,
		Version:    version,
		Type:       typ,
		PURL:       purl,
		Licenses:   []string{},
		LicenseIDs: []string{},
		Locations:  locations,
	}
	if c.Locations == nil {
		c.Locations = []Location{}
	}

	for _, v := range licenses {
		if v == "" || slices.Contains(c.Licenses, v) {
			continue
		}

		c.Licenses = append(c.Licenses, v)
		for _, id := range LicenseIDs(v) {
			if !slices.ContainsFunc(
				c.LicenseIDs,
				func(have string) bool {
					return strings.EqualFold(have, id)
				},
			) {
				c.LicenseIDs = append(c.LicenseIDs, id)
			}
		}
	}

	return c
}

func syftComponents(raw []byte) ([]Component, error) {
	var doc struct {
		Artifacts []struct {
			Name      string            `json:"name"`
			Version   string            `json:"version"`
			Type      string            `json:"type"`
			PURL      string            `json:"purl"`
			Licenses  []json.RawMessage `json:"licenses"`
			Locations []Location        `json:"locations"`
		} `json:"artifacts"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}

	out := make([]Component, 0, len(doc.Artifacts))
	for _, a := range doc.Artifacts {
		licenses := make([]string, 0, len(a.Licenses))
		for _, l := range a.Licenses {
			licenses = append(licenses, licenseValue(l))
		}

		out = append(out, newComponent(a.Name, a.Version, a.Type, a.PURL, licenses, a.Locations))
	}

	return out, nil
}

func licenseValue(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return namedLicense(s)
	}

	var obj struct {
		Value          string `json:"value"`
		SPDXExpression string `json:"spdxExpression"`
	}
	if json.Unmarshal(raw, &obj) != nil {
		return ""
	}

	if obj.SPDXExpression != "" {
		return obj.SPDXExpression
	}

	return namedLicense(obj.Value)
}

func namedLicense(v string) string {
	if strings.HasPrefix(v, "sha256:") || strings.HasPrefix(v, "LicenseRef-sha256") {
		return ""
	}

	return v
}

func isLicenseIDByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '-' || b == '.' || b == '+'
}

// LicenseIDs lists the licenses an SPDX expression names, normalized
// ("apache-2.0 OR mit" names Apache-2.0 and MIT).
func LicenseIDs(expr string) []string {
	if strings.TrimSpace(expr) == "" {
		return []string{}
	}

	ids, err := spdxexp.ExtractLicenses(expr)
	if err != nil || len(ids) == 0 {
		return []string{
			expr,
		}
	}

	lower := strings.ToLower(expr)
	at := func(id string) int {
		id = strings.TrimSuffix(strings.TrimSuffix(strings.ToLower(id), "-or-later"), "-only")

		for from := 0; from < len(lower); {
			i := strings.Index(lower[from:], id)
			if i < 0 {
				break
			}

			i += from
			if i == 0 || !isLicenseIDByte(lower[i-1]) {
				return i
			}

			from = i + 1
		}

		return len(lower)
	}

	slices.SortStableFunc(
		ids,
		func(a, b string) int {
			return cmp.Or(cmp.Compare(at(a), at(b)), strings.Compare(a, b))
		},
	)

	return ids
}
