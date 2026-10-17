package policy

import (
	"slices"
	"strings"
	"time"

	"github.com/github/go-spdx/v2/spdxexp"

	"relic/internal/scanning/scan"
)

// LicenseViolation is a license rule that a component of an artifact
// violates.
type LicenseViolation struct {
	Component int    // index into the components evaluated
	License   string // the component's license expression, "" when it has none
	Match
}

// LicenseViolations evaluates the licenses of an artifact's components
// against the license policies of watches, skipping what ignore rules
// cover.
func LicenseViolations(
	watches []Watch,
	pols map[string]Policy,
	ignores []IgnoreRule,
	subject Subject,
	comps []scan.Component,
	now time.Time,
) []LicenseViolation {
	var out []LicenseViolation

	for i, c := range comps {
		licenses := c.Licenses
		if len(licenses) == 0 {
			licenses = []string{
				"",
			}
		}

		s := subject

		s.Packages = nil
		if c.PURL != "" {
			s.Packages = []string{
				c.PURL,
			}
		}

		for _, l := range licenses {
			s.License = l

			for _, m := range Evaluate(watches, pols, Issue{
				IsLicense: true,
				License:   l,
			}) {
				s.Watch, s.Policy = m.Watch, m.Policy

				if slices.ContainsFunc(ignores, func(r IgnoreRule) bool {
					return r.Ignores(s, now)
				}) {
					continue
				}

				out = append(out, LicenseViolation{
					Component: i,
					License:   l,
					Match:     m,
				})
			}
		}
	}

	return out
}

func bannedLicense(expr string, banned []string) bool {
	ids, err := spdxexp.ExtractLicenses(expr)
	if err != nil {
		terms := strings.FieldsFunc(expr, func(r rune) bool {
			return r == ' ' || r == '(' || r == ')'
		})

		return slices.ContainsFunc(banned, func(b string) bool {
			return strings.EqualFold(b, expr) || slices.ContainsFunc(terms, func(term string) bool {
				return licenseBase(term) == licenseBase(b)
			})
		})
	}

	bases := make([]string, 0, len(banned))
	for _, b := range banned {
		if n, err := spdxexp.ExtractLicenses(b); err == nil && len(n) == 1 {
			b = n[0]
		}

		bases = append(bases, licenseBase(b))
	}

	keep := slices.DeleteFunc(
		slices.Clone(ids),
		func(id string) bool {
			return slices.Contains(bases, licenseBase(id))
		},
	)

	switch {
	case len(keep) == len(ids):
		return false
	case len(keep) == 0:
		return true
	}

	ok, err := spdxexp.Satisfies(expr, keep)

	return err != nil || !ok
}

func allowedLicense(expr string, allowed []string) bool {
	if slices.ContainsFunc(allowed, func(a string) bool {
		return strings.EqualFold(a, expr)
	}) {
		return true
	}

	valid := slices.DeleteFunc(slices.Clone(allowed), func(a string) bool {
		ok, _ := spdxexp.ValidateLicenses([]string{
			a,
		})

		return !ok
	})
	if len(valid) == 0 {
		return false
	}

	ok, err := spdxexp.Satisfies(expr, valid)

	return err == nil && ok
}

func licenseBase(id string) string {
	id = strings.ToLower(strings.TrimSuffix(id, "+"))
	id = strings.TrimSuffix(id, "-or-later")

	return strings.TrimSuffix(id, "-only")
}
