package relicapi

import (
	"slices"
	"testing"

	"relic/internal/delivery/buildinfo"
)

func TestBuildScanRequestScansEveryDependency(t *testing.T) {
	t.Parallel()

	doc := buildinfo.Document{
		Modules: []buildinfo.Module{
			{
				Type: "npm",
				Dependencies: []buildinfo.Dependency{
					{
						ID:     "lodash:4.17.15",
						SHA256: "1111",
					},
					{
						ID:     "lodash-4.17.15.tgz",
						SHA256: "ABCD",
					},
				},
				Artifacts: []buildinfo.Artifact{
					{
						Name:   "app.tgz",
						SHA256: "ef01",
					},
				},
			},
		},
	}

	req, err := buildScanRequest(t.Context(), Dependencies{}, doc)
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(req.PURLs, []string{
		"pkg:npm/lodash@4.17.15",
	}) {
		t.Errorf("purls = %v", req.PURLs)
	}

	if !slices.Equal(req.Artifacts, []string{
		"abcd",
		"ef01",
	}) {
		t.Errorf("content scanned = %v, want the dependency without a package id and the artifact", req.Artifacts)
	}
}
