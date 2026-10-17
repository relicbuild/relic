//go:build integration

package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestScanAndAudit(t *testing.T) {
	t.Parallel()

	srv := scanningServer(t)
	dir := t.TempDir()
	config := filepath.Join(dir, "config.yaml")

	if _, err := relic(
		t,
		config,
		"password\n",
		"auth",
		"login",
		srv.URL,
		"--user",
		"admin",
		"--password-stdin",
	); err != nil {
		t.Fatal(err)
	}

	syft := filepath.Join(dir, "syft")

	//nolint:gosec // a test script
	if err := os.WriteFile(syft, []byte(`#!/bin/sh
echo "$2" >>"$0.targets"
echo '{"artifacts":[{"purl":"pkg:npm/lodash@4.17.15"},{"purl":"pkg:npm/left-pad@1.3.0"},{"purl":""}]}'
`), 0o700); err != nil {
		t.Fatal(err)
	}

	out, err := relic(t, config, "", "security", "scan", "app.jar", "--syft", syft)
	if err != nil || !strings.Contains(out, "app.jar: 2 components, 2 findings (critical 0, high 2") ||
		!slices.ContainsFunc(tableRows(out), func(row []string) bool {
			return len(row) == 5 && row[2] == "pkg:npm/lodash@4.17.15" && row[3] != ""
		}) {
		t.Errorf("scan = %q %v", out, err)
	}

	if _, err := relic(
		t,
		config,
		"",
		"security",
		"scan",
		"app.jar",
		"--syft",
		filepath.Join(dir, "missing"),
	); err == nil ||
		!strings.Contains(err.Error(), "not installed") {
		t.Errorf("scan without syft: %v", err)
	}

	sboms := map[string]struct {
		content    string
		components int
	}{
		"bom.cdx.json": {
			content:    `{"bomFormat":"CycloneDX","components":[{"purl":"pkg:npm/app@1.0.0","components":[{"purl":"pkg:npm/lodash@4.17.15"}]}]}`,
			components: 2,
		},
		"bom.spdx.json": {
			content:    `{"spdxVersion":"SPDX-2.3","packages":[{"externalRefs":[{"referenceType":"purl","referenceLocator":"pkg:npm/lodash@4.17.15"}]}]}`,
			components: 1,
		},
		"bom.syft.json": {
			content:    `{"artifacts":[{"purl":"pkg:npm/lodash@4.17.15"}]}`,
			components: 1,
		},
	}
	for name, sbom := range sboms {
		f := filepath.Join(dir, name)
		if err := os.WriteFile(f, []byte(sbom.content), 0o600); err != nil {
			t.Fatal(err)
		}

		out, err := relic(t, config, "", "security", "scan", "--sbom", f, "-o", "json")

		var res struct {
			Scan struct {
				Components int
				Findings   []struct {
					ID string
				}
			}
		}
		if err != nil || json.Unmarshal([]byte(out), &res) != nil || len(res.Scan.Findings) != 2 ||
			res.Scan.Components != sbom.components {
			t.Errorf("scan --sbom %s = %q %v, want %d components and 2 findings", name, out, err, sbom.components)
		}

		for _, finding := range res.Scan.Findings {
			if finding.ID == "" {
				t.Errorf("scan --sbom %s: a finding has no ID: %q", name, out)
			}
		}
	}

	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"hello":"world"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := relic(t, config, "", "security", "scan", "--sbom", bad); err == nil {
		t.Error("an unknown SBOM format was accepted")
	}

	if _, err := relic(t, config, `apiVersion: relic.build/v1
kind: Repository
metadata: {name: npm-remote}
spec: {class: remote, format: npm, remote: {url: "https://registry.npmjs.org"}}
---
apiVersion: relic.build/v1
kind: Policy
metadata: {name: s-fail}
spec: {kind: security, rules: [{name: any, priority: 1, minSeverity: low, failBuild: true}]}
---
apiVersion: relic.build/v1
kind: Policy
metadata: {name: s-block}
spec: {kind: security, rules: [{name: high, priority: 1, minSeverity: high, blockDownload: true}]}
---
apiVersion: relic.build/v1
kind: Watch
metadata: {name: s-ci}
spec: {active: true, scopes: [{kind: build, names: [none]}], policies: [s-fail]}
---
apiVersion: relic.build/v1
kind: Watch
metadata: {name: s-remote}
spec: {active: true, scopes: [{kind: repository, names: [npm-remote]}], policies: [s-block]}
`, "apply", "-f", "-"); err != nil {
		t.Fatal(err)
	}

	var ec exitCode

	out, err = relic(t, config, "", "security", "scan", "app.jar", "--syft", syft, "--watch", "s-ci")
	if !errors.As(err, &ec) || ec != 1 || !hasTableRowWith(out, "s-ci", "fails builds") {
		t.Errorf("scan --watch = %q %v", out, err)
	}

	project := filepath.Join(dir, "project")

	out, err = relic(t, config, "", "security", "audit", project, "--syft", syft)
	if err != nil || !strings.Contains(out, project+": 2 components") {
		t.Errorf("audit = %q %v", out, err)
	}

	if b, _ := os.ReadFile(syft + ".targets"); !strings.Contains(string(b), "dir:"+project+"\n") {
		t.Errorf("syft targets = %q", b)
	}

	if _, err := relic(t, config, "", "security", "audit", "--curation", "--syft", syft); err == nil {
		t.Error("audit --curation without a repository succeeded")
	}

	out, err = relic(
		t,
		config,
		"",
		"security",
		"audit",
		project,
		"--syft",
		syft,
		"--curation",
		"--repository",
		"npm-remote",
	)
	if !errors.As(err, &ec) || ec != 1 || !strings.Contains(out, "1 packages would be blocked through npm-remote") ||
		!hasTableRowWith(out, "blocks downloads") {
		t.Errorf("audit --curation = %q %v", out, err)
	}
}
