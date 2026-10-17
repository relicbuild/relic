package scan

import (
	"encoding/json"
	"strconv"

	packageurl "github.com/package-url/packageurl-go"
)

// CycloneDX builds a minimal CycloneDX 1.5 document listing packages by
// package URL, for Grype to match.
func CycloneDX(purls []string) ([]byte, error) {
	type component struct {
		Type    string `json:"type"`
		BOMRef  string `json:"bom-ref"`
		Group   string `json:"group,omitempty"`
		Name    string `json:"name"`
		Version string `json:"version"`
		PURL    string `json:"purl"`
	}

	doc := struct {
		BOMFormat   string      `json:"bomFormat"`
		SpecVersion string      `json:"specVersion"`
		Version     int         `json:"version"`
		Components  []component `json:"components"`
	}{
		BOMFormat:   "CycloneDX",
		SpecVersion: "1.5",
		Version:     1,
		Components:  []component{},
	}

	for i, p := range purls {
		u, err := packageurl.FromString(p)
		if err != nil {
			continue
		}

		c := component{
			Type:    "library",
			BOMRef:  "c" + strconv.Itoa(i),
			Name:    u.Name,
			Version: u.Version,
			PURL:    p,
		}
		switch u.Type {
		case packageurl.TypeMaven:
			c.Group = u.Namespace
		case packageurl.TypeNPM, packageurl.TypeGolang, packageurl.TypeComposer:
			if u.Namespace != "" {
				c.Name = u.Namespace + "/" + u.Name
			}
		}

		doc.Components = append(doc.Components, c)
	}

	return json.Marshal(doc)
}

func graphComponents(req GraphRequest) []Component {
	out := make([]Component, 0, len(req.PURLs))
	for _, p := range req.PURLs {
		u, err := packageurl.FromString(p)
		if err != nil {
			continue
		}

		name := u.Name
		if u.Namespace != "" {
			name = u.Namespace + "/" + u.Name
		}

		out = append(out, newComponent(name, u.Version, u.Type, p, req.Licenses[p], nil))
	}

	return out
}
