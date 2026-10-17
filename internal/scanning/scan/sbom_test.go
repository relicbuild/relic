package scan

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"
)

const syftDocument = `{
  "schema": {"version": "16.0.0"},
  "artifacts": [
    {"name": "lodash", "version": "4.17.20", "type": "npm", "purl": "pkg:npm/lodash@4.17.20",
     "licenses": [{"value": "MIT", "spdxExpression": "MIT"}]},
    {"name": "openssl", "version": "3.0.2", "type": "deb", "purl": "pkg:deb/ubuntu/openssl@3.0.2",
     "licenses": ["Apache-2.0 OR MIT"]}
  ]
}`

func names(cs []Component) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Name+"@"+c.Version+" "+c.PURL)
	}

	slices.Sort(out)

	return out
}

func TestExportSBOM(t *testing.T) {
	t.Parallel()

	stored, err := gzipBytes([]byte(syftDocument))
	if err != nil {
		t.Fatal(err)
	}

	want, err := Components(stored)
	if err != nil || len(want) != 2 {
		t.Fatalf("components = %v, %v", want, err)
	}

	for _, format := range []SBOMFormat{
		CycloneDXJSON,
		SPDXJSON,
		SyftJSON,
	} {
		t.Run(string(format), func(t *testing.T) {
			t.Parallel()

			doc, err := ExportSBOM(stored, format, "libs/app.tgz", time.Unix(1_800_000_000, 0))
			if err != nil {
				t.Fatal(err)
			}

			if got, err := DetectSBOM(doc); err != nil || got != format {
				t.Fatalf("exported %s detects as %s, %v", format, got, err)
			}

			got, err := sbomComponents(doc)
			if err != nil || !slices.Equal(names(got), names(want)) {
				t.Errorf("components of the %s export = %v, %v; want %v", format, names(got), err, names(want))
			}

			for _, c := range got {
				if c.Name == "openssl" && !slices.Equal(c.LicenseIDs, []string{
					"Apache-2.0",
					"MIT",
				}) {
					t.Errorf("openssl's licenses in the %s export = %v", format, c.LicenseIDs)
				}
			}
		})
	}
}

func TestImportedSBOMs(t *testing.T) {
	t.Parallel()

	cdx := `{"bomFormat": "CycloneDX", "specVersion": "1.5", "components": [
	  {"type": "library", "name": "log4j-core", "version": "2.14.1", "purl": "pkg:maven/org.apache.logging.log4j/log4j-core@2.14.1",
	   "licenses": [{"license": {"id": "Apache-2.0"}}],
	   "components": [{"type": "library", "name": "log4j-api", "version": "2.14.1"}]}
	]}`
	spdxDoc := `{"spdxVersion": "SPDX-2.3", "packages": [
	  {"SPDXID": "SPDXRef-1", "name": "requests", "versionInfo": "2.25.0", "licenseConcluded": "NOASSERTION",
	   "licenseDeclared": "Apache-2.0",
	   "externalRefs": [{"referenceCategory": "PACKAGE-MANAGER", "referenceType": "purl", "referenceLocator": "pkg:pypi/requests@2.25.0"}]}
	]}`

	tests := []struct {
		name   string
		doc    string
		format SBOMFormat
		want   []string
	}{
		{
			name:   "CycloneDX with nested components",
			doc:    cdx,
			format: CycloneDXJSON,
			want: []string{
				"log4j-api@2.14.1 ",
				"log4j-core@2.14.1 pkg:maven/org.apache.logging.log4j/log4j-core@2.14.1",
			},
		},
		{
			name:   "SPDX",
			doc:    spdxDoc,
			format: SPDXJSON,
			want: []string{
				"requests@2.25.0 pkg:pypi/requests@2.25.0",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got, err := DetectSBOM([]byte(tt.doc)); err != nil || got != tt.format {
				t.Fatalf("format = %s, %v", got, err)
			}

			got, err := sbomComponents([]byte(tt.doc))
			if err != nil || !slices.Equal(names(got), tt.want) {
				t.Errorf("components = %v, %v; want %v", names(got), err, tt.want)
			}

			stored, err := gzipBytes([]byte(tt.doc))
			if err != nil {
				t.Fatal(err)
			}

			if same, err := ExportSBOM(stored, tt.format, "x", time.Now()); err != nil || string(same) != tt.doc {
				t.Errorf("an export in the imported format changed the document: %v", err)
			}

			if _, err := ExportSBOM(stored, SyftJSON, "x", time.Now()); !errors.Is(err, ErrSBOM) {
				t.Errorf("an imported document exported as Syft JSON: %v", err)
			}
		})
	}

	for _, bad := range []string{
		`not json`,
		`{"hello": "world"}`,
	} {
		if _, err := DetectSBOM([]byte(bad)); !errors.Is(err, ErrSBOM) {
			t.Errorf("DetectSBOM(%q) = %v", bad, err)
		}
	}
}

func TestExportedLicenses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		licenses  []string
		cycloneDX string
		spdx      string
		extracted []string
	}{
		{
			name: "a listed license",
			licenses: []string{
				"mit",
			},
			cycloneDX: `[{"license":{"id":"MIT"}}]`,
			spdx:      "MIT",
		},
		{
			name: "a license name",
			licenses: []string{
				"The Apache Software License, Version 2.0",
			},
			cycloneDX: `[{"license":{"name":"The Apache Software License, Version 2.0"}}]`,
			spdx:      "LicenseRef-The-Apache-Software-License-Version-2.0",
			extracted: []string{
				"LicenseRef-The-Apache-Software-License-Version-2.0=The Apache Software License, Version 2.0",
			},
		},
		{
			name: "a listed license and a name",
			licenses: []string{
				"Custom, see LICENSE file",
				"MIT",
			},
			cycloneDX: `[{"license":{"name":"Custom, see LICENSE file"}},{"license":{"id":"MIT"}}]`,
			spdx:      "LicenseRef-Custom-see-LICENSE-file AND MIT",
			extracted: []string{
				"LicenseRef-Custom-see-LICENSE-file=Custom, see LICENSE file",
			},
		},
		{
			name: "an expression and a listed license",
			licenses: []string{
				"MIT OR Apache-2.0",
				"BSD-3-Clause",
			},
			cycloneDX: `[{"expression":"(MIT OR Apache-2.0) AND BSD-3-Clause"}]`,
			spdx:      "(MIT OR Apache-2.0) AND BSD-3-Clause",
		},
		{
			name: "a license reference",
			licenses: []string{
				"LicenseRef-Custom",
			},
			cycloneDX: `[{"expression":"LicenseRef-Custom"}]`,
			spdx:      "LicenseRef-Custom",
			extracted: []string{
				"LicenseRef-Custom=NOASSERTION",
			},
		},
		{
			name: "a license with an exception",
			licenses: []string{
				"GPL-2.0-or-later WITH Classpath-exception-2.0",
			},
			cycloneDX: `[{"expression":"GPL-2.0-or-later WITH Classpath-exception-2.0"}]`,
			spdx:      "GPL-2.0-or-later WITH Classpath-exception-2.0",
		},
		{
			name: "an expression and a name",
			licenses: []string{
				"MIT OR Apache-2.0",
				"Custom",
			},
			cycloneDX: `[{"expression":"(MIT OR Apache-2.0) AND LicenseRef-Custom"}]`,
			spdx:      "(MIT OR Apache-2.0) AND LicenseRef-Custom",
			extracted: []string{
				"LicenseRef-Custom=Custom",
			},
		},
		{
			name:      "no license",
			cycloneDX: `null`,
			spdx:      "NOASSERTION",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			comps := []Component{
				newComponent("foo", "1", "library", "pkg:npm/foo@1", tt.licenses, nil),
			}
			now := time.Unix(1_800_000_000, 0)

			cdxDoc, err := cycloneDXDocument(comps, "libs/foo", now)
			if err != nil {
				t.Fatal(err)
			}

			var bom struct {
				Components []struct {
					Licenses json.RawMessage `json:"licenses"`
				} `json:"components"`
			}
			if err := json.Unmarshal(cdxDoc, &bom); err != nil {
				t.Fatal(err)
			}

			got := string(bom.Components[0].Licenses)
			if got == "" {
				got = "null"
			}

			if compact(t, got) != compact(t, tt.cycloneDX) {
				t.Errorf("CycloneDX licenses = %s, want %s", got, tt.cycloneDX)
			}

			spdxDoc, err := spdxDocument(comps, "libs/foo", now)
			if err != nil {
				t.Fatal(err)
			}

			var spdx struct {
				Packages []struct {
					LicenseDeclared string `json:"licenseDeclared"`
				} `json:"packages"`
				Extracted []struct {
					LicenseID     string `json:"licenseId"`
					ExtractedText string `json:"extractedText"`
				} `json:"hasExtractedLicensingInfos"`
			}
			if err := json.Unmarshal(spdxDoc, &spdx); err != nil {
				t.Fatal(err)
			}

			if spdx.Packages[0].LicenseDeclared != tt.spdx {
				t.Errorf("SPDX licenseDeclared = %q, want %q", spdx.Packages[0].LicenseDeclared, tt.spdx)
			}

			extracted := make([]string, 0, len(spdx.Extracted))
			for _, e := range spdx.Extracted {
				extracted = append(extracted, e.LicenseID+"="+e.ExtractedText)
			}

			if !slices.Equal(extracted, tt.extracted) {
				t.Errorf("SPDX extracted licenses = %q, want %q", extracted, tt.extracted)
			}
		})
	}
}

func compact(t *testing.T, s string) string {
	t.Helper()

	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}

	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}

	return string(b)
}

func TestSPDXExportOfNoComponentsHasEmptyRelationships(t *testing.T) {
	t.Parallel()

	stored, err := gzipBytes([]byte(`{"bomFormat":"CycloneDX","specVersion":"1.6","version":1,"components":[]}`))
	if err != nil {
		t.Fatal(err)
	}

	doc, err := ExportSBOM(stored, SPDXJSON, "libs/empty", time.Unix(1_800_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}

	var got struct {
		Packages      []json.RawMessage `json:"packages"`
		Relationships []json.RawMessage `json:"relationships"`
	}
	if err := json.Unmarshal(doc, &got); err != nil {
		t.Fatal(err)
	}

	if got.Packages == nil || got.Relationships == nil {
		t.Errorf("SPDX export of no components = %s, want empty packages and relationships arrays", doc)
	}
}

func TestExportSBOMKeepsTheOperatingSystem(t *testing.T) {
	t.Parallel()

	stored, err := gzipBytes([]byte(`{"bomFormat":"CycloneDX","specVersion":"1.6","components":[
		{"type":"operating-system","name":"ubuntu","version":"22.04"},
		{"type":"library","name":"lodash","version":"4.17.20","purl":"pkg:npm/lodash@4.17.20"}]}`))
	if err != nil {
		t.Fatal(err)
	}

	doc, err := ExportSBOM(stored, SPDXJSON, "libs/image", time.Unix(1_800_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}

	got, err := sbomComponents(doc)
	if err != nil {
		t.Fatal(err)
	}

	if !slices.ContainsFunc(got, func(c Component) bool {
		return c.Name == "ubuntu" && c.Type == "operating-system"
	}) {
		t.Errorf("components of the SPDX export = %+v, want ubuntu as the operating system", got)
	}
}
