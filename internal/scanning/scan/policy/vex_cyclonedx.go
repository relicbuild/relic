package policy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	cdx "github.com/CycloneDX/cyclonedx-go"
	packageurl "github.com/package-url/packageurl-go"
)

// VEX formats.
const (
	VEXFormatOpenVEX   = "openvex"
	VEXFormatCycloneDX = "cyclonedx"
)

// OpenVEX justifications and their CycloneDX counterparts.
var cycloneDXJustifications = map[string]cdx.ImpactAnalysisJustification{
	"component_not_present":                             cdx.IAJCodeNotPresent,
	"vulnerable_code_not_present":                       cdx.IAJCodeNotPresent,
	"vulnerable_code_not_in_execute_path":               cdx.IAJCodeNotReachable,
	"vulnerable_code_cannot_be_controlled_by_adversary": cdx.IAJProtectedByMitigatingControl,
	"inline_mitigations_already_exist":                  cdx.IAJProtectedByMitigatingControl,
}

// openVEXJustifications maps CycloneDX justifications to OpenVEX ones.
var openVEXJustifications = map[cdx.ImpactAnalysisJustification]string{
	cdx.IAJCodeNotPresent:               "vulnerable_code_not_present",
	cdx.IAJCodeNotReachable:             "vulnerable_code_not_in_execute_path",
	cdx.IAJRequiresConfiguration:        "vulnerable_code_cannot_be_controlled_by_adversary",
	cdx.IAJRequiresDependency:           "vulnerable_code_cannot_be_controlled_by_adversary",
	cdx.IAJRequiresEnvironment:          "vulnerable_code_cannot_be_controlled_by_adversary",
	cdx.IAJProtectedByCompiler:          "inline_mitigations_already_exist",
	cdx.IAJProtectedAtRuntime:           "inline_mitigations_already_exist",
	cdx.IAJProtectedAtPerimeter:         "inline_mitigations_already_exist",
	cdx.IAJProtectedByMitigatingControl: "inline_mitigations_already_exist",
}

// CycloneDXVEX writes doc's statements as a CycloneDX 1.6 VEX document: a
// vulnerability per statement, affecting the package URLs it names (a
// product's subcomponents, or the product itself).
func CycloneDXVEX(doc VEXDocument) ([]byte, error) {
	vulns := make([]cdx.Vulnerability, 0, len(doc.Statements))

	for _, s := range doc.Statements {
		state := cdx.IASNotAffected
		if s.Status == "fixed" {
			state = cdx.IASResolved
		}

		packages := statementPackages(s)
		affects := make([]cdx.Affects, 0, len(packages))

		for _, p := range packages {
			affects = append(affects, cdx.Affects{
				Ref: p,
			})
		}

		refs := make([]cdx.VulnerabilityReference, 0, len(s.Vulnerability.Aliases))

		for _, a := range s.Vulnerability.Aliases {
			refs = append(refs, cdx.VulnerabilityReference{
				ID: a,
			})
		}

		detail := s.ImpactStatement
		if s.StatusNotes != "" {
			detail += " (" + s.StatusNotes + ")"
		}

		v := cdx.Vulnerability{
			ID: s.Vulnerability.Name,
			Analysis: &cdx.VulnerabilityAnalysis{
				State:         state,
				Justification: cycloneDXJustifications[s.Justification],
				Detail:        detail,
			},
			Affects: &affects,
		}
		if len(refs) > 0 {
			v.References = &refs
		}

		vulns = append(vulns, v)
	}

	bom := cdx.NewBOM()
	bom.SpecVersion = cdx.SpecVersion1_6
	bom.SerialNumber = doc.ID
	bom.Metadata = &cdx.Metadata{
		Timestamp: doc.Timestamp.UTC().Format(time.RFC3339),
		Authors: &[]cdx.OrganizationalContact{
			{
				Name: doc.Author,
			},
		},
		Tools: &cdx.ToolsChoice{
			Components: &[]cdx.Component{
				{
					Type: cdx.ComponentTypeApplication,
					Name: "Relic",
				},
			},
		},
	}
	bom.Vulnerabilities = &vulns

	var buf bytes.Buffer

	enc := cdx.NewBOMEncoder(&buf, cdx.BOMFileFormatJSON)
	enc.SetPretty(true)

	if err := enc.EncodeVersion(bom, cdx.SpecVersion1_6); err != nil {
		return nil, fmt.Errorf("write CycloneDX VEX: %w", err)
	}

	return buf.Bytes(), nil
}

// statementPackages are the package URLs a statement names: its products'
// subcomponents, or the products themselves.
func statementPackages(s VEXStatement) []string {
	var out []string

	for _, p := range s.Products {
		targets := p.Subcomponents
		if len(targets) == 0 {
			targets = []VEXProduct{
				p,
			}
		}

		for _, t := range targets {
			if _, err := packageurl.FromString(t.ID); err == nil && !slices.Contains(out, t.ID) {
				out = append(out, t.ID)
			}
		}
	}

	return out
}

// ParseAnyVEX reads an OpenVEX document, or a CycloneDX one as the OpenVEX
// statements it makes: not_affected stays not_affected, resolved becomes
// fixed, and other states keep their CycloneDX name (ImportVEX leaves them
// out).
func ParseAnyVEX(b []byte) (VEXDocument, error) {
	var probe struct {
		BOMFormat string `json:"bomFormat"`
	}

	if json.Unmarshal(b, &probe) != nil || probe.BOMFormat != "CycloneDX" {
		return ParseVEX(b)
	}

	var bom cdx.BOM
	if err := cdx.NewBOMDecoder(bytes.NewReader(b), cdx.BOMFileFormatJSON).Decode(&bom); err != nil {
		return VEXDocument{}, fmt.Errorf("%w: %w", ErrInvalidVEX, err)
	}

	doc := VEXDocument{
		ID:     bom.SerialNumber,
		Author: cycloneDXAuthor(bom),
	}

	if bom.Vulnerabilities == nil {
		return doc, fmt.Errorf("%w: a CycloneDX document without vulnerabilities", ErrInvalidVEX)
	}

	for _, v := range *bom.Vulnerabilities {
		s := VEXStatement{
			Vulnerability: VEXVulnerability{
				Name: v.ID,
			},
		}

		if v.References != nil {
			for _, r := range *v.References {
				if r.ID != "" && r.ID != v.ID {
					s.Vulnerability.Aliases = append(s.Vulnerability.Aliases, r.ID)
				}
			}
		}

		if v.Affects != nil {
			for _, a := range *v.Affects {
				s.Products = append(s.Products, VEXProduct{
					ID: a.Ref,
				})
			}
		}

		if v.Analysis != nil {
			switch v.Analysis.State {
			case cdx.IASNotAffected:
				s.Status = "not_affected"
			case cdx.IASResolved, cdx.IASResolvedWithPedigree:
				s.Status = "fixed"
			default:
				s.Status = string(v.Analysis.State)
			}

			s.Justification = openVEXJustifications[v.Analysis.Justification]
			s.ImpactStatement = v.Analysis.Detail
		}

		doc.Statements = append(doc.Statements, s)
	}

	return doc, nil
}

func cycloneDXAuthor(bom cdx.BOM) string {
	if bom.Metadata != nil && bom.Metadata.Authors != nil {
		for _, a := range *bom.Metadata.Authors {
			if a.Name != "" {
				return a.Name
			}
		}
	}

	return "an unnamed author"
}
