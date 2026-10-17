package policy

import (
	"slices"
	"testing"
	"time"

	"relic/internal/scanning/scan"
)

func TestLicenseViolations(t *testing.T) {
	t.Parallel()

	no := false
	pols := map[string]Policy{
		"no-gpl": {
			Name: "no-gpl",
			Kind: KindLicense,
			Rules: []Rule{
				{
					Name: "ban",
					BannedLicenses: []string{
						"GPL-3.0-only",
					},
					BlockDownload: true,
				},
			},
		},
		"known": {
			Name: "known",
			Kind: KindLicense,
			Rules: []Rule{
				{
					Name: "permissive",
					AllowedLicenses: []string{
						"MIT",
						"Apache-2.0",
					},
					AllowUnknown: &no,
				},
			},
		},
		"sec": {
			Name: "sec",
			Kind: KindSecurity,
			Rules: []Rule{
				{
					Name:        "any",
					MinSeverity: scan.SeverityLow,
				},
			},
		},
	}
	watches := []Watch{
		{
			Name:   "w",
			Active: true,
			Policies: []string{
				"no-gpl",
				"known",
				"sec",
			},
		},
	}
	comps := []scan.Component{
		{
			Name: "ok",
			PURL: "pkg:npm/ok@1",
			Licenses: []string{
				"MIT OR GPL-3.0-only",
			},
		},
		{
			Name: "gpl",
			PURL: "pkg:npm/gpl@1",
			Licenses: []string{
				"MIT AND GPL-3.0-only",
			},
		},
		{
			Name: "none",
			PURL: "pkg:npm/none@1",
		},
		{
			Name: "bsd",
			PURL: "pkg:npm/bsd@1",
			Licenses: []string{
				"BSD-3-Clause",
			},
		},
	}
	now := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		ignores []IgnoreRule
		want    []string // "component/policy/license"
	}{
		{
			name: "each component is evaluated by its license",
			want: []string{
				"gpl/no-gpl/MIT AND GPL-3.0-only",
				"gpl/known/MIT AND GPL-3.0-only",
				"none/known/",
				"bsd/known/BSD-3-Clause",
			},
		},
		{
			name: "an ignore rule by package",
			ignores: []IgnoreRule{
				{
					Packages: []string{
						"pkg:npm/gpl",
					},
				},
			},
			want: []string{
				"none/known/",
				"bsd/known/BSD-3-Clause",
			},
		},
		{
			name: "an ignore rule by one license of an expression",
			ignores: []IgnoreRule{
				{
					Licenses: []string{
						"gpl-3.0-only",
					},
				},
			},
			want: []string{
				"none/known/",
				"bsd/known/BSD-3-Clause",
			},
		},
		{
			name: "an ignore rule by policy",
			ignores: []IgnoreRule{
				{
					Policies: []string{
						"known",
					},
				},
			},
			want: []string{
				"gpl/no-gpl/MIT AND GPL-3.0-only",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var got []string
			for _, v := range LicenseViolations(watches, pols, tt.ignores, Subject{}, comps, now) {
				got = append(got, comps[v.Component].Name+"/"+v.Policy+"/"+v.License)
			}

			if !slices.Equal(got, tt.want) {
				t.Errorf("LicenseViolations = %q, want %q", got, tt.want)
			}
		})
	}
}
