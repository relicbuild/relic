package policy

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	packageurl "github.com/package-url/packageurl-go"

	"relic/internal/scanning/scan"
)

// OpenVEXContext is the OpenVEX version Relic writes and reads.
const OpenVEXContext = "https://openvex.dev/ns/v0.2.0"

// VEXDocument is an OpenVEX document.
type VEXDocument struct {
	Context    string         `json:"@context"`
	ID         string         `json:"@id"`
	Author     string         `json:"author"`
	Timestamp  time.Time      `json:"timestamp"`
	Version    int            `json:"version"`
	Tooling    string         `json:"tooling,omitempty"`
	Statements []VEXStatement `json:"statements"`
}

// VEXStatement says how a vulnerability affects products.
type VEXStatement struct {
	Vulnerability   VEXVulnerability `json:"vulnerability"`
	Products        []VEXProduct     `json:"products"`
	Status          string           `json:"status"`
	Justification   string           `json:"justification,omitempty"`
	ImpactStatement string           `json:"impact_statement,omitempty"`
	StatusNotes     string           `json:"status_notes,omitempty"`
}

// VEXVulnerability names a vulnerability.
type VEXVulnerability struct {
	Name    string   `json:"name"`
	Aliases []string `json:"aliases,omitempty"`
}

// VEXProduct is a product, or a component of one, by package URL: its @id,
// or its identifiers when the @id is another IRI.
type VEXProduct struct {
	ID            string          `json:"@id"`
	Identifiers   *VEXIdentifiers `json:"identifiers,omitempty"`
	Subcomponents []VEXProduct    `json:"subcomponents,omitempty"`
}

// VEXIdentifiers name a product other than by its @id.
type VEXIdentifiers struct {
	PURL string `json:"purl,omitempty"`
}

func (p VEXProduct) packageURL() (string, bool) {
	candidates := []string{
		p.ID,
	}
	if p.Identifiers != nil {
		candidates = append(candidates, p.Identifiers.PURL)
	}

	for _, c := range candidates {
		if ValidPackage(c) {
			return c, true
		}
	}

	return "", false
}

// ErrInvalidVEX is a document Relic cannot read as OpenVEX.
var ErrInvalidVEX = errors.New("not an OpenVEX document")

// ExportVEX writes the ignore rules that name vulnerabilities in packages,
// and nothing VEX cannot state (licenses, watches, policies, paths, builds or
// an expiry), as OpenVEX not_affected statements, one per vulnerability, or
// one per rule with its other IDs as aliases when the rule names both CVEs
// and others.
func ExportVEX(rules []IgnoreRule, id, author string, now time.Time) (VEXDocument, int) {
	doc := VEXDocument{
		Context:    OpenVEXContext,
		ID:         id,
		Author:     author,
		Timestamp:  now.UTC(),
		Version:    1,
		Tooling:    "Relic",
		Statements: []VEXStatement{},
	}
	skipped := 0

	for _, r := range rules {
		vulns := slices.Compact(slices.Sorted(slices.Values(append(slices.Clone(r.Vulnerabilities), r.CVEs...))))

		var products []VEXProduct

		for _, p := range r.Packages {
			if _, err := packageurl.FromString(p); err == nil {
				products = append(products, VEXProduct{
					ID: p,
				})
			}
		}

		if !r.statable() || len(vulns) == 0 || len(products) == 0 {
			skipped++

			continue
		}

		impact := r.Notes
		if impact == "" {
			impact = "Ignored in Relic"
		}

		statement := func(v VEXVulnerability) VEXStatement {
			return VEXStatement{
				Vulnerability:   v,
				Products:        products,
				Status:          "not_affected",
				ImpactStatement: impact,
				StatusNotes:     relicRuleNote + r.ID,
			}
		}

		if len(r.CVEs) > 0 && len(r.Vulnerabilities) > 0 {
			doc.Statements = append(doc.Statements, statement(VEXVulnerability{
				Name:    r.CVEs[0],
				Aliases: slices.Concat(r.CVEs[1:], r.Vulnerabilities),
			}))

			continue
		}

		for _, v := range vulns {
			doc.Statements = append(doc.Statements, statement(VEXVulnerability{
				Name: v,
			}))
		}
	}

	return doc, skipped
}

func (r IgnoreRule) statable() bool {
	return r.Expires == nil &&
		len(r.Licenses)+len(r.Watches)+len(r.Policies)+len(r.ArtifactPaths)+len(r.Builds) == 0
}

var cveID = regexp.MustCompile(`(?i)^CVE-\d{4}-\d+$`)

const relicRuleNote = "Relic ignore rule "

// vexRuleIDs is what a statement names as a rule does: its vulnerability and
// aliases, split into CVEs and other IDs when ExportVEX wrote it from a rule
// that needs both.
func vexRuleIDs(s VEXStatement) (vulnerabilities, cves []string) {
	ids := slices.Concat([]string{
		s.Vulnerability.Name,
	}, s.Vulnerability.Aliases)

	if !strings.Contains(s.StatusNotes+s.ImpactStatement, relicRuleNote) {
		return ids, nil
	}

	cves = slices.DeleteFunc(slices.Clone(ids), func(id string) bool {
		return !cveID.MatchString(id)
	})
	others := slices.DeleteFunc(slices.Clone(ids), cveID.MatchString)

	if len(cves) == 0 || len(others) == 0 {
		return ids, nil
	}

	return others, cves
}

// UnreachableStatements are not_affected statements for the vulnerabilities
// govulncheck found no call path to: the scanned file is the product (a
// generic package URL with its checksum) and the Go module the
// subcomponent, since another file may call the same module's vulnerable
// code.
func UnreachableStatements(found []scan.UnreachableFinding) []VEXStatement {
	out := make([]VEXStatement, 0, len(found))

	for _, f := range found {
		name := f.FileName
		if name == "" {
			name = "artifact"
		}

		product := packageurl.NewPackageURL(
			packageurl.TypeGeneric,
			"",
			name,
			"",
			packageurl.Qualifiers{
				{
					Key:   "checksum",
					Value: "sha256:" + f.Sha256,
				},
			},
			"",
		).ToString()

		out = append(out, VEXStatement{
			Vulnerability: VEXVulnerability{
				Name: f.VulnID,
				Aliases: slices.DeleteFunc(slices.Clone(f.CVEs), func(c string) bool {
					return c == f.VulnID
				}),
			},
			Products: []VEXProduct{
				{
					ID: product,
					Subcomponents: []VEXProduct{
						{
							ID: f.PURL,
						},
					},
				},
			},
			Status:          "not_affected",
			Justification:   "vulnerable_code_not_in_execute_path",
			ImpactStatement: "govulncheck found no call path from the executable's code to the vulnerable functions",
			StatusNotes:     "Relic reachability analysis",
		})
	}

	return out
}

// ParseVEX reads an OpenVEX document.
func ParseVEX(b []byte) (VEXDocument, error) {
	var doc VEXDocument
	if err := json.Unmarshal(b, &doc); err != nil {
		return doc, fmt.Errorf("%w: %w", ErrInvalidVEX, err)
	}

	if !strings.HasPrefix(doc.Context, "https://openvex.dev/ns") {
		return doc, fmt.Errorf("%w: @context %q", ErrInvalidVEX, doc.Context)
	}

	return doc, nil
}

// ImportVEX turns a document's not_affected and fixed statements into
// ignore rules on the vulnerability in the packages they name (a product's
// subcomponents, or the product itself), noting the document and its
// author.
func ImportVEX(doc VEXDocument) ([]IgnoreRule, int) {
	var (
		rules   []IgnoreRule
		skipped int
	)

	for _, s := range doc.Statements {
		packages := statementPackages(s)

		if (s.Status != "not_affected" && s.Status != "fixed") || s.Vulnerability.Name == "" || len(packages) == 0 {
			skipped++

			continue
		}

		why := strings.TrimSpace(strings.Join([]string{
			s.Status,
			s.Justification,
			s.ImpactStatement,
		}, " "))

		vulnerabilities, cves := vexRuleIDs(s)

		rules = append(rules, IgnoreRule{
			Notes:           fmt.Sprintf("VEX %s by %s: %s", doc.ID, doc.Author, why),
			Vulnerabilities: vulnerabilities,
			CVEs:            cves,
			Packages:        packages,
		})
	}

	return rules, skipped
}
