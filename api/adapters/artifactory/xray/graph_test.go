package xray

import (
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"relic/internal/scanning/scan"
)

func TestImpactPathsOfAWideGraphEnd(t *testing.T) {
	t.Parallel()

	const (
		levels = 70
		width  = 4
	)

	req := graphRequest{
		Root:    "root",
		Parents: map[string][]string{},
	}

	for level := range levels - 1 {
		for i := range width {
			for j := range width {
				node := fmt.Sprintf("n%d-%d", level, i)
				req.Parents[node] = append(req.Parents[node], fmt.Sprintf("n%d-%d", level+1, j))
			}
		}
	}

	done := make(chan [][]ImpactNode, 1)
	go func() {
		done <- impactPaths(req, "n0-0")
	}()

	select {
	case paths := <-done:
		if len(paths) > 20 {
			t.Errorf("%d impact paths, want at most 20", len(paths))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("impactPaths did not end on a wide, deep graph")
	}
}

func TestGraphOfBuildScanWithoutTree(t *testing.T) {
	t.Parallel()

	g, err := graphOf(scan.GraphScan{
		Request: scan.GraphRequest{
			PURLs: []string{
				"pkg:npm/lodash@4.17.20",
				"pkg:maven/org.example/app@1.0",
			},
			Artifacts: []string{
				"abc",
			},
		},
		Build: &scan.GraphBuild{
			Name:   "app",
			Number: "7",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if g.Root != "build://app:7" || !slices.Equal(g.Artifacts, []string{
		"abc",
	}) {
		t.Errorf("root %q, artifacts %v", g.Root, g.Artifacts)
	}

	for purl, id := range map[string]string{
		"pkg:npm/lodash@4.17.20":        "npm://lodash:4.17.20",
		"pkg:maven/org.example/app@1.0": "gav://org.example:app:1.0",
	} {
		if !slices.Equal(g.IDsByPURL[purl], []string{
			id,
		}) || !slices.Equal(g.Parents[id], []string{
			g.Root,
		}) {
			t.Errorf("%s: ids %v, parents %v", purl, g.IDsByPURL[purl], g.Parents[id])
		}
	}
}

func TestImpactPathsCarryNodePaths(t *testing.T) {
	t.Parallel()

	req := graphRequest{
		Root: "generic://sha256:abc/app.tar",
		Parents: map[string][]string{
			"npm://lodash:4.17.20": {
				"generic://sha256:abc/app.tar",
			},
		},
		Paths: map[string]string{
			"generic://sha256:abc/app.tar": "app.tar",
			"npm://lodash:4.17.20":         "app.tar/node_modules/lodash/package.json",
		},
	}

	want := [][]ImpactNode{
		{
			{
				ComponentID: "generic://sha256:abc/app.tar",
				FullPath:    "app.tar",
			},
			{
				ComponentID: "npm://lodash:4.17.20",
				FullPath:    "app.tar/node_modules/lodash/package.json",
			},
		},
	}
	if got := impactPaths(req, "npm://lodash:4.17.20"); !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("impact paths = %+v, want %+v", got, want)
	}
}

func TestCVEsKeepTheirOwnScores(t *testing.T) {
	t.Parallel()

	got := cves(scan.Finding{
		ID: "GHSA-aaaa-bbbb-cccc",
		Aliases: []string{
			"CVE-2024-0001",
			"CVE-2024-0002",
		},
		CVSS: []scan.CVSS{
			{
				Version: "3.1",
				Vector:  "CVSS:3.1/AV:N",
				Score:   9.8,
			},
		},
		AliasCVSS: map[string][]scan.CVSS{
			"CVE-2024-0001": {
				{
					Version: "3.1",
					Vector:  "CVSS:3.1/AV:L",
					Score:   3.3,
				},
				{
					Version: "2.0",
					Vector:  "AV:L",
					Score:   2.1,
				},
			},
		},
	})

	want := []CVE{
		{
			CVE:          "CVE-2024-0001",
			CVSSV2Score:  "2.1",
			CVSSV2Vector: "AV:L",
			CVSSV3Score:  "3.3",
			CVSSV3Vector: "CVSS:3.1/AV:L",
		},
		{
			CVE:          "CVE-2024-0002",
			CVSSV3Score:  "9.8",
			CVSSV3Vector: "CVSS:3.1/AV:N",
		},
	}
	if !slices.Equal(got, want) {
		t.Errorf("cves = %+v, want %+v", got, want)
	}
}

func TestGraphResultLicenses(t *testing.T) {
	t.Parallel()

	req := graphRequest{
		Root: "root",
		IDsByPURL: map[string][]string{
			"pkg:npm/lodash@4.17.15": {
				"npm://lodash:4.17.15",
			},
			"pkg:pypi/requests@2.19.0": {
				"pypi://requests:2.19.0",
			},
		},
		Parents: map[string][]string{
			"npm://lodash:4.17.15": {
				"root",
			},
			"pypi://requests:2.19.0": {
				"root",
			},
		},
	}
	rep := scan.Report{
		Components: []scan.Component{
			{
				PURL: "pkg:npm/lodash@4.17.15",
				LicenseIDs: []string{
					"MIT",
					"Apache-2.0",
				},
			},
			{
				PURL:       "pkg:pypi/requests@2.19.0",
				LicenseIDs: []string{},
			},
			{
				PURL: "pkg:npm/not-in-the-graph@1.0.0",
				LicenseIDs: []string{
					"GPL-3.0-only",
				},
			},
		},
	}

	path := func(id string) LicenseComponent {
		return LicenseComponent{
			ImpactPaths: [][]ImpactNode{
				{
					{
						ComponentID: "root",
					},
					{
						ComponentID: id,
					},
				},
			},
		}
	}
	license := func(key string, ids ...string) License {
		l := License{
			LicenseKey: key,
			Name:       key,
			References: []string{},
			Components: map[string]LicenseComponent{},
		}
		for _, id := range ids {
			l.Components[id] = path(id)
		}

		return l
	}

	tests := []struct {
		name     string
		licenses bool
		want     []License
	}{
		{
			name:     "licenses asked for",
			licenses: true,
			want: []License{
				license("Apache-2.0", "npm://lodash:4.17.15"),
				license("MIT", "npm://lodash:4.17.15"),
				license("Unknown", "pypi://requests:2.19.0"),
			},
		},
		{
			name: "licenses not asked for",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := buildResult("id", req, rep, true, tt.licenses).Licenses
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("licenses = %+v, want %+v", got, tt.want)
			}
		})
	}
}
