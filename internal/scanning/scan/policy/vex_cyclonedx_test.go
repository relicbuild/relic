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

	refs, err := ParseAnyVEX([]byte(`{"bomFormat":"CycloneDX","specVersion":"1.6",
		"serialNumber":"urn:uuid:3e671687-395b-41f5-a30f-a58921a69b79","version":1,
		"components":[
		 {"type":"library","bom-ref":"component-1","name":"minimist","version":"0.0.8","purl":"pkg:npm/minimist@0.0.8"},
		 {"type":"application","bom-ref":"app","name":"app","components":[
		  {"type":"library","bom-ref":"nested lib","name":"a","version":"1","purl":"pkg:npm/a@1"}]}],
		"vulnerabilities":[
		 {"id":"CVE-1","analysis":{"state":"not_affected"},"affects":[{"ref":"component-1"}]},
		 {"id":"CVE-2","analysis":{"state":"not_affected"},
		  "affects":[{"ref":"urn:cdx:3e671687-395b-41f5-a30f-a58921a69b79/1#component-1"}]},
		 {"id":"CVE-3","analysis":{"state":"not_affected"},"affects":[{"ref":"urn:cdx:3e671687-395b-41f5-a30f-a58921a69b79/1#nested%20lib"}]},
		 {"id":"CVE-4","analysis":{"state":"not_affected"},
		  "affects":[{"ref":"urn:cdx:00000000-0000-0000-0000-000000000000/1#component-1"}]},
		 {"id":"CVE-5","analysis":{"state":"not_affected"},"affects":[{"ref":"app"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}

	rules, skipped = ImportVEX(refs)

	packages := map[string][]string{}
	for _, r := range rules {
		packages[r.Vulnerabilities[0]] = r.Packages
	}

	want := map[string][]string{
		"CVE-1": {
			"pkg:npm/minimist@0.0.8",
		},
		"CVE-2": {
			"pkg:npm/minimist@0.0.8",
		},
		"CVE-3": {
			"pkg:npm/a@1",
		},
	}
	if skipped != 2 || len(packages) != len(want) {
		t.Errorf("rules from references = %+v, skipped %d; want %v and 2 skipped", packages, skipped, want)
	}

	for id, purls := range want {
		if !slices.Equal(packages[id], purls) {
			t.Errorf("%s affects %v, want %v", id, packages[id], purls)
		}
	}

	if _, err := ParseAnyVEX([]byte(`{"@context":"https://example.com"}`)); err == nil {
		t.Error("a document that is neither format was read")
	}

	if _, err := ParseAnyVEX([]byte(`{"bomFormat":"CycloneDX","specVersion":"1.6"}`)); err == nil {
		t.Error("a CycloneDX document without vulnerabilities was read as VEX")
	}
}
