package scan

import (
	"reflect"
	"testing"
)

func TestComponents(t *testing.T) {
	t.Parallel()

	sbom := `{"artifacts":[
		{"name":"zlib","version":"1.2","type":"apk","purl":"pkg:apk/alpine/zlib@1.2","licenses":[{"value":"Zlib","spdxExpression":"Zlib"}],
		 "locations":[{"path":"/lib/apk/db/installed","layerID":"sha256:aa"}]},
		{"name":"busybox","version":"1.36","type":"apk","licenses":["GPL-2.0-only","GPL-2.0-only"]},
		{"name":"left-pad","version":"1.3.0","type":"npm","licenses":[{"value":"MIT"},{"value":"sha256:fd7e4aae0e0e"},"sha256:ab"],"locations":[{"path":"/app/package-lock.json"}]},
		{"name":"dual","version":"2","type":"npm","licenses":[{"value":"MIT OR Apache-2.0","spdxExpression":"MIT OR Apache-2.0"},{"value":"mit"}]}
	]}`

	stored, err := gzipBytes([]byte(sbom))
	if err != nil {
		t.Fatal(err)
	}

	got, err := Components(stored)
	if err != nil {
		t.Fatal(err)
	}

	want := []Component{
		{
			Name:    "busybox",
			Version: "1.36",
			Type:    "apk",
			Licenses: []string{
				"GPL-2.0-only",
			},
			LicenseIDs: []string{
				"GPL-2.0-only",
			},
			Locations: []Location{},
		},
		{
			Name:    "dual",
			Version: "2",
			Type:    "npm",
			Licenses: []string{
				"MIT OR Apache-2.0",
				"mit",
			},
			LicenseIDs: []string{
				"MIT",
				"Apache-2.0",
			},
			Locations: []Location{},
		},
		{
			Name:    "left-pad",
			Version: "1.3.0",
			Type:    "npm",
			Licenses: []string{
				"MIT",
			},
			LicenseIDs: []string{
				"MIT",
			},
			Locations: []Location{
				{
					Path: "/app/package-lock.json",
				},
			},
		},
		{
			Name:    "zlib",
			Version: "1.2",
			Type:    "apk",
			PURL:    "pkg:apk/alpine/zlib@1.2",
			Licenses: []string{
				"Zlib",
			},
			LicenseIDs: []string{
				"Zlib",
			},
			Locations: []Location{
				{
					Path:    "/lib/apk/db/installed",
					LayerID: "sha256:aa",
				},
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Components =\n%+v\nwant\n%+v", got, want)
	}

	if _, err := Components([]byte("not gzip")); err == nil {
		t.Error("Components(garbage) succeeded")
	}
}

func TestLicenseIDs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, expr string
		want       []string
	}{
		{
			"one license",
			"MIT",
			[]string{
				"MIT",
			},
		},
		{
			"case is normalized",
			"apache-2.0",
			[]string{
				"Apache-2.0",
			},
		},
		{
			"a license whose ID holds another's",
			"AGPL-3.0-only AND BSD-3-Clause AND GPL-3.0-only",
			[]string{
				"AGPL-3.0-only",
				"BSD-3-Clause",
				"GPL-3.0-only",
			},
		},
		{
			"an expression",
			"(MIT OR Apache-2.0) AND BSD-3-Clause",
			[]string{
				"MIT",
				"Apache-2.0",
				"BSD-3-Clause",
			},
		},
		{
			"plus is or-later",
			"GPL-2.0+",
			[]string{
				"GPL-2.0-or-later",
			},
		},
		{
			"a name that is not SPDX",
			"Apache 2.0",
			[]string{
				"Apache 2.0",
			},
		},
		{
			"nothing",
			"",
			[]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := LicenseIDs(tt.expr); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("LicenseIDs(%q) = %q, want %q", tt.expr, got, tt.want)
			}
		})
	}
}
