//go:build integration

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVexCommands(t *testing.T) {
	t.Parallel()

	config, dir := adminCLI(t)

	doc := `{"@context": "https://openvex.dev/ns/v0.2.0", "@id": "https://vendor.example/vex/7", "author": "Vendor",
  "timestamp": "2026-09-01T00:00:00Z", "version": 1,
  "statements": [
    {"vulnerability": {"name": "CVE-2021-23337"}, "products": [{"@id": "pkg:npm/lodash@4.17.15"}], "status": "not_affected",
     "justification": "vulnerable_code_not_in_execute_path"},
    {"vulnerability": {"name": "CVE-2023-9"}, "products": [{"@id": "pkg:npm/a@1"}], "status": "under_investigation"}
  ]}`

	out, err := relic(t, config, doc, "security", "vex", "import", "-")
	if err != nil || !hasTableRowWith(out, "CVE-2021-23337", "pkg:npm/lodash@4.17.15") ||
		!strings.HasSuffix(out, "1 ignore rules created, 0 already stored, 1 statements left out\n") {
		t.Fatalf("import = %q %v", out, err)
	}

	file := filepath.Join(dir, "relic.vex.json")
	if _, err := relic(t, config, "", "security", "vex", "export", "--file", file); err != nil {
		t.Fatal(err)
	}

	out, err = relic(t, config, "", "security", "vex", "export")
	if err != nil || !strings.Contains(out, `"name": "CVE-2021-23337"`) ||
		!strings.Contains(out, `"@context": "https://openvex.dev/ns/v0.2.0"`) {
		t.Errorf("export = %q %v", out, err)
	}

	if out, err := relic(
		t,
		config,
		"",
		"security",
		"vex",
		"import",
		file,
	); err != nil ||
		!strings.HasSuffix(out, "0 ignore rules created, 1 already stored, 0 statements left out\n") {
		t.Errorf("re-import = %q %v", out, err)
	}

	cdxFile := filepath.Join(dir, "relic.cdx.json")
	if _, err := relic(
		t,
		config,
		"",
		"security",
		"vex",
		"export",
		"--format",
		"cyclonedx",
		"--file",
		cdxFile,
	); err != nil {
		t.Fatal(err)
	}

	cdx, err := os.ReadFile(cdxFile)
	if err != nil || !strings.Contains(string(cdx), `"bomFormat": "CycloneDX"`) ||
		!strings.Contains(string(cdx), `"ref": "pkg:npm/lodash@4.17.15"`) {
		t.Errorf("CycloneDX export = %s %v", cdx, err)
	}

	if out, err := relic(t, config, "", "security", "vex", "import", cdxFile); err != nil ||
		!strings.HasSuffix(out, "0 ignore rules created, 1 already stored, 0 statements left out\n") {
		t.Errorf("CycloneDX re-import = %q %v", out, err)
	}

	if _, err := relic(t, config, "", "security", "vex", "export", "--format", "spdx"); err == nil {
		t.Error("an unknown format was accepted")
	}

	if _, err := relic(t, config, `{"hello": 1}`, "security", "vex", "import", "-"); err == nil {
		t.Error("a document that is not OpenVEX was imported")
	}
}
