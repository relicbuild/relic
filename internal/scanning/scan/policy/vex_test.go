package policy

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestVEXRoundTrip(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	tomorrow := now.Add(24 * time.Hour)
	rules := []IgnoreRule{
		{
			ID:    "r1",
			Notes: "not reachable from our code",
			CVEs: []string{
				"CVE-2021-23337",
			},
			Vulnerabilities: []string{
				"GHSA-35jh-r3h4-6jhm",
			},
			Packages: []string{
				"pkg:npm/lodash@4.17.15",
			},
		},
		{
			ID: "expired",
			CVEs: []string{
				"CVE-1",
			},
			Packages: []string{
				"pkg:npm/a@1",
			},
			Expires: &past,
		},
		{
			ID: "license only",
			Licenses: []string{
				"GPL-3.0-only",
			},
		},
		{
			ID: "no package",
			CVEs: []string{
				"CVE-2",
			},
			ArtifactPaths: []string{
				"libs/app.jar",
			},
		},
		{
			ID: "until tomorrow",
			CVEs: []string{
				"CVE-3",
			},
			Packages: []string{
				"pkg:npm/b@1",
			},
			Expires: &tomorrow,
		},
		{
			ID: "one watch",
			CVEs: []string{
				"CVE-4",
			},
			Packages: []string{
				"pkg:npm/c@1",
			},
			Watches: []string{
				"prod",
			},
		},
		{
			ID: "one policy",
			CVEs: []string{
				"CVE-5",
			},
			Packages: []string{
				"pkg:npm/d@1",
			},
			Policies: []string{
				"critical",
			},
		},
		{
			ID: "one path",
			CVEs: []string{
				"CVE-6",
			},
			Packages: []string{
				"pkg:npm/e@1",
			},
			ArtifactPaths: []string{
				"libs/only-here/",
			},
		},
		{
			ID: "one build",
			CVEs: []string{
				"CVE-7",
			},
			Packages: []string{
				"pkg:npm/f@1",
			},
			Builds: []Build{
				{
					Name: "app",
				},
			},
		},
	}

	doc, skipped := ExportVEX(rules, "https://relic.example/vex/1", "relic.example", now)
	if skipped != 8 || len(doc.Statements) != 1 || doc.Context != OpenVEXContext {
		t.Fatalf("export: %d statements, %d skipped: %+v", len(doc.Statements), skipped, doc)
	}

	s := doc.Statements[0]
	if s.Status != "not_affected" || s.ImpactStatement != "not reachable from our code" ||
		s.Products[0].ID != "pkg:npm/lodash@4.17.15" {
		t.Errorf("statement = %+v", s)
	}

	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}

	back, err := ParseVEX(b)
	if err != nil {
		t.Fatal(err)
	}

	imported, skipped := ImportVEX(back)
	if skipped != 0 || len(imported) != 1 {
		t.Fatalf("import: %d rules, %d skipped", len(imported), skipped)
	}

	got := imported[0]
	if !slices.Equal(got.Packages, []string{
		"pkg:npm/lodash@4.17.15",
	}) || !strings.Contains(got.Notes, "VEX https://relic.example/vex/1 by relic.example: not_affected") ||
		got.Empty() {
		t.Errorf("rule = %+v", got)
	}

	subject := Subject{
		VulnerabilityIDs: []string{
			"GHSA-35jh-r3h4-6jhm",
		},
		CVEs: []string{
			"CVE-2021-23337",
		},
		Packages: []string{
			"pkg:npm/lodash@4.17.15",
		},
	}

	if !slices.ContainsFunc(imported, func(r IgnoreRule) bool {
		return r.Ignores(subject, now)
	}) {
		t.Error("the imported rules do not ignore the finding")
	}

	if !slices.Equal(got.CVEs, rules[0].CVEs) || !slices.Equal(got.Vulnerabilities, rules[0].Vulnerabilities) {
		t.Errorf("imported rule names CVEs %v and vulnerabilities %v, want the exported rule's %v and %v",
			got.CVEs, got.Vulnerabilities, rules[0].CVEs, rules[0].Vulnerabilities)
	}

	onlyTheAdvisory := Subject{
		VulnerabilityIDs: []string{
			"GHSA-35jh-r3h4-6jhm",
		},
		Packages: []string{
			"pkg:npm/lodash@4.17.15",
		},
	}
	if rules[0].Ignores(onlyTheAdvisory, now) != got.Ignores(onlyTheAdvisory, now) {
		t.Errorf("the round trip changed whether the rule ignores a finding without its CVE")
	}
}

func TestImportVEX(t *testing.T) {
	t.Parallel()

	doc, err := ParseVEX([]byte(`{
  "@context": "https://openvex.dev/ns/v0.2.0",
  "@id": "https://vendor.example/vex/42",
  "author": "Vendor Security",
  "timestamp": "2026-09-01T00:00:00Z",
  "version": 1,
  "statements": [
    {"vulnerability": {"name": "CVE-2023-1"}, "products": [{"@id": "pkg:oci/app@sha256%3Aabc", "subcomponents": [{"@id": "pkg:golang/golang.org/x/net@v0.10.0"}]}],
     "status": "not_affected", "justification": "vulnerable_code_not_in_execute_path"},
    {"vulnerability": {"name": "CVE-2023-2"}, "products": [{"@id": "pkg:npm/a@1"}], "status": "affected"},
    {"vulnerability": {"name": "CVE-2023-3"}, "products": [{"@id": "https://example.com/not-a-purl"}], "status": "not_affected"},
    {"vulnerability": {"name": "CVE-2020-7598"}, "products": [{"@id": "https://example.com/product", "identifiers": {"purl": "pkg:npm/minimist@0.0.8"}}],
     "status": "not_affected", "justification": "vulnerable_code_not_in_execute_path"}
  ]
}`))
	if err != nil {
		t.Fatal(err)
	}

	rules, skipped := ImportVEX(doc)
	if len(rules) != 2 || skipped != 2 {
		t.Fatalf("rules %+v, skipped %d", rules, skipped)
	}

	if !slices.Equal(rules[0].Packages, []string{
		"pkg:golang/golang.org/x/net@v0.10.0",
	}) || !strings.Contains(rules[0].Notes, "vulnerable_code_not_in_execute_path") {
		t.Errorf("rule = %+v", rules[0])
	}

	if !slices.Equal(rules[1].Packages, []string{
		"pkg:npm/minimist@0.0.8",
	}) {
		t.Errorf("rule of a product named by identifiers.purl = %+v", rules[1])
	}

	if _, err := ParseVEX([]byte(`{"@context": "https://cyclonedx.org"}`)); err == nil {
		t.Error("a document that is not OpenVEX was read")
	}
}
