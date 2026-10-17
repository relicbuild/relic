package scan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"uuid"

	"relic/internal/platform/database"
)

// SBOMFormat is a format SBOMs are imported and exported in.
type SBOMFormat string

// SBOM formats, all JSON.
const (
	SyftJSON      SBOMFormat = "syft"
	CycloneDXJSON SBOMFormat = "cyclonedx"
	SPDXJSON      SBOMFormat = "spdx"
)

// ErrSBOM reports a document that is not an SBOM Relic reads, or a format it
// cannot write.
var ErrSBOM = errors.New("invalid SBOM")

// MaxSBOMSize bounds an imported SBOM.
const MaxSBOMSize = 64 << 20

// DetectSBOM names the format of an SBOM document.
func DetectSBOM(raw []byte) (SBOMFormat, error) {
	var probe struct {
		BOMFormat   string          `json:"bomFormat"`
		SPDXVersion string          `json:"spdxVersion"`
		Artifacts   json.RawMessage `json:"artifacts"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return "", fmt.Errorf("%w: not a JSON document", ErrSBOM)
	}

	switch {
	case strings.EqualFold(probe.BOMFormat, "CycloneDX"):
		return CycloneDXJSON, nil
	case strings.HasPrefix(probe.SPDXVersion, "SPDX-"):
		return SPDXJSON, nil
	case len(probe.Artifacts) > 0:
		return SyftJSON, nil
	}

	return "", fmt.Errorf("%w: not a CycloneDX, SPDX or Syft JSON document", ErrSBOM)
}

func sbomComponents(raw []byte) ([]Component, error) {
	format, err := DetectSBOM(raw)
	if err != nil {
		return nil, err
	}

	switch format {
	case CycloneDXJSON:
		return cycloneDXComponents(raw)
	case SPDXJSON:
		return spdxComponents(raw)
	default:
		return syftComponents(raw)
	}
}

type cdxLicense struct {
	License *struct {
		ID   string `json:"id,omitempty"`
		Name string `json:"name,omitempty"`
	} `json:"license,omitempty"`
	Expression string `json:"expression,omitempty"`
}

type cdxComponent struct {
	Type       string         `json:"type"`
	BOMRef     string         `json:"bom-ref,omitempty"`
	Name       string         `json:"name"`
	Version    string         `json:"version,omitempty"`
	PURL       string         `json:"purl,omitempty"`
	Licenses   []cdxLicense   `json:"licenses,omitempty"`
	Components []cdxComponent `json:"components,omitempty"`
}

func cycloneDXComponents(raw []byte) ([]Component, error) {
	var doc struct {
		Components []cdxComponent `json:"components"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSBOM, err)
	}

	var (
		out  []Component
		walk func([]cdxComponent)
	)

	walk = func(cs []cdxComponent) {
		for _, c := range cs {
			licenses := make([]string, 0, len(c.Licenses))
			for _, l := range c.Licenses {
				switch {
				case l.Expression != "":
					licenses = append(licenses, l.Expression)
				case l.License != nil && l.License.ID != "":
					licenses = append(licenses, l.License.ID)
				case l.License != nil:
					licenses = append(licenses, l.License.Name)
				}
			}

			out = append(out, newComponent(c.Name, c.Version, c.Type, c.PURL, licenses, nil))
			walk(c.Components)
		}
	}
	walk(doc.Components)

	return out, nil
}

type spdxPackage struct {
	SPDXID           string `json:"SPDXID"`
	Name             string `json:"name"`
	VersionInfo      string `json:"versionInfo,omitempty"`
	DownloadLocation string `json:"downloadLocation"`
	LicenseConcluded string `json:"licenseConcluded,omitempty"`
	LicenseDeclared  string `json:"licenseDeclared,omitempty"`
	PrimaryPurpose   string `json:"primaryPackagePurpose,omitempty"`
	ExternalRefs     []struct {
		ReferenceCategory string `json:"referenceCategory"`
		ReferenceType     string `json:"referenceType"`
		ReferenceLocator  string `json:"referenceLocator"`
	} `json:"externalRefs,omitempty"`
}

func spdxLicense(v string) string {
	if v == "" || v == "NOASSERTION" || v == "NONE" {
		return ""
	}

	return v
}

func spdxComponents(raw []byte) ([]Component, error) {
	var doc struct {
		Packages []spdxPackage `json:"packages"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSBOM, err)
	}

	out := make([]Component, 0, len(doc.Packages))
	for _, p := range doc.Packages {
		var purl string

		for _, ref := range p.ExternalRefs {
			if strings.EqualFold(ref.ReferenceType, "purl") {
				purl = ref.ReferenceLocator
			}
		}

		licenses := []string{
			spdxLicense(p.LicenseConcluded),
		}
		if declared := spdxLicense(p.LicenseDeclared); declared != licenses[0] {
			licenses = append(licenses, declared)
		}

		out = append(out, newComponent(p.Name, p.VersionInfo, strings.ToLower(p.PrimaryPurpose), purl, licenses, nil))
	}

	return out, nil
}

// ExportSBOM writes a stored (gzipped) SBOM in format, for subject (what the
// SBOM describes, such as repository/path). A document already in format is
// returned as stored; Syft JSON is only available for SBOMs Relic made.
func ExportSBOM(stored []byte, format SBOMFormat, subject string, now time.Time) ([]byte, error) {
	raw, err := gunzip(stored)
	if err != nil {
		return nil, fmt.Errorf("read stored SBOM: %w", err)
	}

	have, err := DetectSBOM(raw)
	if err != nil {
		return nil, err
	}

	if have == format {
		return raw, nil
	}

	comps, err := sbomComponents(raw)
	if err != nil {
		return nil, err
	}

	switch format {
	case CycloneDXJSON:
		return cycloneDXDocument(comps, subject, now)
	case SPDXJSON:
		return spdxDocument(comps, subject, now)
	default:
		return nil, fmt.Errorf("%w: this SBOM was imported as %s; export it as cyclonedx or spdx", ErrSBOM, have)
	}
}

func licenseChoices(c Component) []cdxLicense {
	out := make([]cdxLicense, 0, len(c.Licenses))
	for _, l := range c.Licenses {
		if ids := LicenseIDs(l); len(ids) == 1 && ids[0] == l {
			out = append(out, cdxLicense{
				License: &struct {
					ID   string `json:"id,omitempty"`
					Name string `json:"name,omitempty"`
				}{
					ID: l,
				},
			})

			continue
		}

		out = append(out, cdxLicense{
			Expression: l,
		})
	}

	return out
}

// cycloneDXDocument writes components as a CycloneDX 1.6 JSON document.
func cycloneDXDocument(comps []Component, subject string, now time.Time) ([]byte, error) {
	type metadata struct {
		Timestamp string `json:"timestamp"`
		Tools     struct {
			Components []cdxComponent `json:"components"`
		} `json:"tools"`
		Component cdxComponent `json:"component"`
	}

	doc := struct {
		BOMFormat    string         `json:"bomFormat"`
		SpecVersion  string         `json:"specVersion"`
		SerialNumber string         `json:"serialNumber"`
		Version      int            `json:"version"`
		Metadata     metadata       `json:"metadata"`
		Components   []cdxComponent `json:"components"`
	}{
		BOMFormat:    "CycloneDX",
		SpecVersion:  "1.6",
		SerialNumber: "urn:uuid:" + uuid.New().String(),
		Version:      1,
		Metadata: metadata{
			Timestamp: now.UTC().Format(time.RFC3339),
			Component: cdxComponent{
				Type:   "file",
				BOMRef: "subject",
				Name:   subject,
			},
		},
		Components: make([]cdxComponent, 0, len(comps)),
	}
	doc.Metadata.Tools.Components = []cdxComponent{
		{
			Type: "application",
			Name: "relic",
		},
	}

	for i, c := range comps {
		typ := "library"
		if c.Type == "operating-system" {
			typ = c.Type
		}

		doc.Components = append(doc.Components, cdxComponent{
			Type:     typ,
			BOMRef:   fmt.Sprintf("component-%d", i+1),
			Name:     c.Name,
			Version:  c.Version,
			PURL:     c.PURL,
			Licenses: licenseChoices(c),
		})
	}

	return json.MarshalIndent(doc, "", "  ")
}

// spdxDocument writes components as an SPDX 2.3 JSON document.
func spdxDocument(comps []Component, subject string, now time.Time) ([]byte, error) {
	type relationship struct {
		Element string `json:"spdxElementId"`
		Type    string `json:"relationshipType"`
		Related string `json:"relatedSpdxElement"`
	}

	type externalRef struct {
		ReferenceCategory string `json:"referenceCategory"`
		ReferenceType     string `json:"referenceType"`
		ReferenceLocator  string `json:"referenceLocator"`
	}

	type pkg struct {
		SPDXID           string        `json:"SPDXID"`
		Name             string        `json:"name"`
		VersionInfo      string        `json:"versionInfo,omitempty"`
		DownloadLocation string        `json:"downloadLocation"`
		FilesAnalyzed    bool          `json:"filesAnalyzed"`
		LicenseConcluded string        `json:"licenseConcluded"`
		LicenseDeclared  string        `json:"licenseDeclared"`
		PrimaryPurpose   string        `json:"primaryPackagePurpose,omitempty"`
		ExternalRefs     []externalRef `json:"externalRefs,omitempty"`
	}

	doc := struct {
		SPDXVersion       string `json:"spdxVersion"`
		DataLicense       string `json:"dataLicense"`
		SPDXID            string `json:"SPDXID"`
		Name              string `json:"name"`
		DocumentNamespace string `json:"documentNamespace"`
		CreationInfo      struct {
			Created  string   `json:"created"`
			Creators []string `json:"creators"`
		} `json:"creationInfo"`
		Packages      []pkg          `json:"packages"`
		Relationships []relationship `json:"relationships"`
	}{
		SPDXVersion:       "SPDX-2.3",
		DataLicense:       "CC0-1.0",
		SPDXID:            "SPDXRef-DOCUMENT",
		Name:              subject,
		DocumentNamespace: "https://relic.build/spdx/" + uuid.New().String(),
		Packages:          make([]pkg, 0, len(comps)),
		Relationships:     make([]relationship, 0, len(comps)),
	}
	doc.CreationInfo.Created = now.UTC().Format(time.RFC3339)
	doc.CreationInfo.Creators = []string{
		"Tool: relic",
	}

	for i, c := range comps {
		id := fmt.Sprintf("SPDXRef-Package-%d", i+1)
		license := "NOASSERTION"

		if len(c.Licenses) > 0 {
			license = strings.Join(c.Licenses, " AND ")
		}

		p := pkg{
			SPDXID:           id,
			Name:             c.Name,
			VersionInfo:      c.Version,
			DownloadLocation: "NOASSERTION",
			LicenseConcluded: "NOASSERTION",
			LicenseDeclared:  license,
		}
		if c.Type == "operating-system" {
			p.PrimaryPurpose = "OPERATING-SYSTEM"
		}

		if c.PURL != "" {
			p.ExternalRefs = []externalRef{
				{
					ReferenceCategory: "PACKAGE-MANAGER",
					ReferenceType:     "purl",
					ReferenceLocator:  c.PURL,
				},
			}
		}

		doc.Packages = append(doc.Packages, p)
		doc.Relationships = append(doc.Relationships, relationship{
			Element: "SPDXRef-DOCUMENT",
			Type:    "DESCRIBES",
			Related: id,
		})
	}

	return json.MarshalIndent(doc, "", "  ")
}

// ImportSBOM replaces the SBOM of content sha256Hex, named fileName, with a
// CycloneDX, SPDX or Syft JSON document, and clears its findings and
// operational risks until the document is matched and assessed again.
func (r *Results) ImportSBOM(ctx context.Context, sha256Hex, fileName string, raw []byte) error {
	comps, err := sbomComponents(raw)
	if err != nil {
		return err
	}

	packed, err := gzipBytes(raw)
	if err != nil {
		return err
	}

	return r.db.WithTx(ctx, func(q database.Querier) error {
		if err := q.ImportScanSbom(ctx, database.ImportScanSbomParams{
			Sha256:     sha256Hex,
			FileName:   fileName,
			Sbom:       packed,
			Components: int32(min(len(comps), 1<<30)), //nolint:gosec // bounded
			Now:        time.Now(),
		}); err != nil {
			return fmt.Errorf("import SBOM of %s: %w", sha256Hex, err)
		}

		if err := q.DeleteOperationalRisks(ctx, sha256Hex); err != nil {
			return err
		}

		if err := q.SetRisksChecked(ctx, database.SetRisksCheckedParams{
			Sha256: sha256Hex,
		}); err != nil {
			return err
		}

		return q.DeleteFindings(ctx, sha256Hex)
	})
}
