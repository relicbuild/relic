package policy

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestCycloneDXVEXRoundTrip(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	rules := []IgnoreRule{
		{
			ID:    "r1",
			Notes: "not used at runtime",
			Vulnerabilities: []string{
				"GHSA-35jh-r3h4-6jhm",
			},
			CVEs: []string{
				"CVE-2021-23337",
			},
			Packages: []string{
				"pkg:npm/lodash@4.17.15",
			},
		},
	}

	doc, skipped := ExportVEX(rules, "urn:uuid:2b5ba9e0-6f6a-4b8e-9b0e-6a6c7a6b8b01", "admin", now)
	if skipped != 0 || len(doc.Statements) != 1 {
		t.Fatalf("statements = %d, skipped %d", len(doc.Statements), skipped)
	}

	doc.Statements = append(doc.Statements, VEXStatement{
		Vulnerability: VEXVulnerability{
			Name: "GHSA-69ch-w2m2-3vjp",
			Aliases: []string{
				"CVE-2022-32149",
			},
		},
		Products: []VEXProduct{
			{
				ID: "pkg:generic/app?checksum=sha256:aa",
				Subcomponents: []VEXProduct{
					{
						ID: "pkg:golang/golang.org/x/text@v0.3.6",
					},
				},
			},
		},
		Status:          "not_affected",
		Justification:   "vulnerable_code_not_in_execute_path",
		ImpactStatement: "govulncheck found no call path",
	})

	b, err := CycloneDXVEX(doc)
	if err != nil {
		t.Fatal(err)
	}

	text := string(b)
	for _, want := range []string{
		`"bomFormat": "CycloneDX"`,
		`"specVersion": "1.6"`,
		`"state": "not_affected"`,
		`"justification": "code_not_reachable"`,
		`"ref": "pkg:golang/golang.org/x/text@v0.3.6"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("document lacks %s:\n%s", want, text)
		}
	}

	back, err := ParseAnyVEX(b)
	if err != nil {
		t.Fatal(err)
	}

	imported, skipped := ImportVEX(back)
	if skipped != 0 || len(imported) != 2 {
		t.Fatalf("imported %d rules, skipped %d", len(imported), skipped)
	}

	last := imported[1]
	module := []string{
		"pkg:golang/golang.org/x/text@v0.3.6",
	}
	named := slices.Contains(last.Vulnerabilities, "GHSA-69ch-w2m2-3vjp") &&
		slices.Contains(last.Vulnerabilities, "CVE-2022-32149")
	noted := strings.Contains(last.Notes, "vulnerable_code_not_in_execute_path") &&
		strings.Contains(last.Notes, "by admin")

	if !named || !noted || !slices.Equal(last.Packages, module) {
		t.Errorf("imported rule = %+v", last)
	}

	got := imported[0]
	if !slices.Equal(got.Vulnerabilities, rules[0].Vulnerabilities) || !slices.Equal(got.CVEs, rules[0].CVEs) ||
		!slices.Equal(got.Packages, []string{
			"pkg:npm/lodash@4.17.15",
		}) || !strings.Contains(got.Notes, "not used at runtime") ||
		!strings.Contains(got.Notes, relicRuleNote+rules[0].ID) {
		t.Errorf("imported rule = %+v, want the exported rule's IDs in lodash and its provenance", got)
	}
}

func TestParseCycloneDXVEXStates(t *testing.T) {
	t.Parallel()

	doc, err := ParseAnyVEX([]byte(`{"bomFormat":"CycloneDX","specVersion":"1.5","serialNumber":"urn:uuid:1",
		"metadata":{"authors":[{"name":"vendor"}]},
		"vulnerabilities":[
		 {"id":"CVE-1","analysis":{"state":"resolved"},"affects":[{"ref":"pkg:npm/a@1.0.0"}]},
		 {"id":"CVE-2","analysis":{"state":"exploitable"},"affects":[{"ref":"pkg:npm/b@1.0.0"}]},
		 {"id":"CVE-3","analysis":{"state":"not_affected","justification":"requires_environment"},"affects":[{"ref":"not a purl"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}

	rules, skipped := ImportVEX(doc)
	if len(rules) != 1 || skipped != 2 || rules[0].Packages[0] != "pkg:npm/a@1.0.0" ||
		!strings.Contains(rules[0].Notes, "fixed") || !strings.Contains(rules[0].Notes, "by vendor") {
		t.Errorf("rules = %+v, skipped %d; want the resolved one only", rules, skipped)
	}

	if _, err := ParseAnyVEX([]byte(`{"@context":"https://example.com"}`)); err == nil {
		t.Error("a document that is neither format was read")
	}

	if _, err := ParseAnyVEX([]byte(`{"bomFormat":"CycloneDX","specVersion":"1.6"}`)); err == nil {
		t.Error("a CycloneDX document without vulnerabilities was read as VEX")
	}
}
