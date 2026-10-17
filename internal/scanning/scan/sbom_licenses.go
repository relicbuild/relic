package scan

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"

	"github.com/github/go-spdx/v2/spdxexp"
)

// licenseTerm is one license of a component as an SBOM can state it.
type licenseTerm struct {
	// listedID is a license on the SPDX License List.
	listedID string
	// expression is any other valid SPDX license expression.
	expression string
	// name is text no SPDX expression can hold.
	name string
}

func parseLicenseTerm(l string) licenseTerm {
	l = strings.TrimSpace(l)
	if ok, id := spdxexp.ActiveLicense(l); ok {
		return licenseTerm{
			listedID: id,
		}
	}

	normalized, invalid := spdxexp.ValidateAndNormalizeLicensesWithOptions(
		[]string{
			l,
		},
		spdxexp.ValidateLicensesOptions{},
	)
	if len(invalid) == 0 && len(normalized) == 1 {
		return licenseTerm{
			expression: normalized[0],
		}
	}

	return licenseTerm{
		name: l,
	}
}

var notLicenseRefByte = regexp.MustCompile(`[^A-Za-z0-9.]+`)

// spdxTerm is the term as SPDX states it: a name as a LicenseRef.
func (t licenseTerm) spdxTerm() string {
	switch {
	case t.listedID != "":
		return t.listedID
	case t.expression != "":
		return t.expression
	}

	ref := strings.Trim(notLicenseRefByte.ReplaceAllString(t.name, "-"), "-")
	if ref == "" {
		sum := sha256.Sum256([]byte(t.name))
		ref = hex.EncodeToString(sum[:8])
	}

	return "LicenseRef-" + ref
}

// componentLicenses are the parsed terms of a component's licenses.
type componentLicenses []licenseTerm

func parseComponentLicenses(licenses []string) componentLicenses {
	out := make(componentLicenses, 0, len(licenses))
	for _, l := range licenses {
		if strings.TrimSpace(l) != "" {
			out = append(out, parseLicenseTerm(l))
		}
	}

	return out
}

// spdxExpression joins the terms with AND, compound terms in parentheses,
// or is NOASSERTION without terms.
func (c componentLicenses) spdxExpression() string {
	if len(c) == 0 {
		return "NOASSERTION"
	}

	terms := make([]string, 0, len(c))
	for _, t := range c {
		term := t.spdxTerm()
		if len(c) > 1 && strings.Contains(term, " ") {
			term = "(" + term + ")"
		}

		terms = append(terms, term)
	}

	return strings.Join(terms, " AND ")
}

// cycloneDXLicenses are the terms as CycloneDX licenses: listed IDs and
// names as licenses, or else one expression of them all.
func (c componentLicenses) cycloneDXLicenses() []cdxLicense {
	if len(c) == 0 {
		return nil
	}

	out := make([]cdxLicense, 0, len(c))
	for _, t := range c {
		if t.expression != "" {
			return []cdxLicense{
				{
					Expression: c.spdxExpression(),
				},
			}
		}

		out = append(out, cdxLicense{
			License: &cdxLicenseChoice{
				ID:   t.listedID,
				Name: t.name,
			},
		})
	}

	return out
}

// spdxLicenseReference defines a LicenseRef an SPDX document uses.
type spdxLicenseReference struct {
	LicenseID     string `json:"licenseId"`
	ExtractedText string `json:"extractedText"`
	Name          string `json:"name,omitempty"`
}

var licenseRef = regexp.MustCompile(`LicenseRef-[A-Za-z0-9.-]+`)

// spdxLicenseReferences define each LicenseRef the terms use once, with the
// name it stands for, or NOASSERTION for one the SBOM named as such.
func (c componentLicenses) spdxLicenseReferences(defined map[string]bool) []spdxLicenseReference {
	var out []spdxLicenseReference

	for _, t := range c {
		for _, ref := range licenseRef.FindAllString(t.spdxTerm(), -1) {
			if defined[ref] {
				continue
			}

			defined[ref] = true

			text := t.name
			if text == "" {
				text = "NOASSERTION"
			}

			out = append(out, spdxLicenseReference{
				LicenseID:     ref,
				ExtractedText: text,
				Name:          t.name,
			})
		}
	}

	return out
}
