package policy

import (
	"testing"
	"time"
)

func TestIgnoreRule(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	s := Subject{
		VulnerabilityIDs: []string{
			"RELIC-ABC",
			"GHSA-1",
		},
		CVEs: []string{
			"CVE-2020-1",
		},
		Watch:  "w",
		Policy: "p",
		Packages: []string{
			"pkg:npm/lodash@4.17.15",
		},
		ArtifactPath: "libs/a/b.jar",
		BuildName:    "app",
		BuildNumber:  "7",
	}

	tests := []struct {
		name string
		rule IgnoreRule
		want bool
	}{
		{
			"empty rule ignores nothing",
			IgnoreRule{},
			false,
		},
		{
			"cve",
			IgnoreRule{
				CVEs: []string{
					"cve-2020-1",
				},
			},
			true,
		},
		{
			"issue id",
			IgnoreRule{
				Vulnerabilities: []string{
					"RELIC-ABC",
				},
			},
			true,
		},
		{
			"vulnerabilities also match cves",
			IgnoreRule{
				Vulnerabilities: []string{
					"CVE-2020-1",
				},
			},
			true,
		},
		{
			"other cve",
			IgnoreRule{
				CVEs: []string{
					"CVE-9",
				},
			},
			false,
		},
		{
			"package any version",
			IgnoreRule{
				Packages: []string{
					"pkg:npm/lodash",
				},
			},
			true,
		},
		{
			"package that version",
			IgnoreRule{
				Packages: []string{
					"pkg:npm/lodash@4.17.15",
				},
			},
			true,
		},
		{
			"package other version",
			IgnoreRule{
				Packages: []string{
					"pkg:npm/lodash@4.17.21",
				},
			},
			false,
		},
		{
			"other package type",
			IgnoreRule{
				Packages: []string{
					"pkg:pypi/lodash",
				},
			},
			false,
		},
		{
			"cve and watch",
			IgnoreRule{
				CVEs: []string{
					"CVE-2020-1",
				},
				Watches: []string{
					"w",
				},
			},
			true,
		},
		{
			"cve but other watch",
			IgnoreRule{
				CVEs: []string{
					"CVE-2020-1",
				},
				Watches: []string{
					"x",
				},
			},
			false,
		},
		{
			"artifact path prefix",
			IgnoreRule{
				ArtifactPaths: []string{
					"libs/a/",
				},
			},
			true,
		},
		{
			"artifact folder without a trailing slash",
			IgnoreRule{
				ArtifactPaths: []string{
					"libs/a",
				},
			},
			true,
		},
		{
			"the artifact itself",
			IgnoreRule{
				ArtifactPaths: []string{
					"/libs/a/b.jar",
				},
			},
			true,
		},
		{
			"a sibling name prefix",
			IgnoreRule{
				ArtifactPaths: []string{
					"libs/a/b",
				},
			},
			false,
		},
		{
			"a repository name prefix",
			IgnoreRule{
				ArtifactPaths: []string{
					"lib",
				},
			},
			false,
		},
		{
			"build any number",
			IgnoreRule{
				Builds: []Build{
					{
						Name: "app",
					},
				},
			},
			true,
		},
		{
			"build other number",
			IgnoreRule{
				Builds: []Build{
					{
						Name:   "app",
						Number: "8",
					},
				},
			},
			false,
		},
		{
			"expired",
			IgnoreRule{
				Expires: &past,
				CVEs: []string{
					"CVE-2020-1",
				},
			},
			false,
		},
		{
			"not yet expired",
			IgnoreRule{
				Expires: &future,
				CVEs: []string{
					"CVE-2020-1",
				},
			},
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := tt.rule.Ignores(s, now); got != tt.want {
				t.Errorf("Ignores = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPackageMatches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		filter, pkg string
		want        bool
	}{
		{
			"pkg:maven/org.apache/log4j",
			"pkg:maven/org.apache/log4j@2.14.1",
			true,
		},
		{
			"pkg:maven/org.apache/log4j@2.14.1",
			"pkg:maven/org.apache/log4j@2.14.1",
			true,
		},
		{
			"pkg:maven/org.apache/log4j",
			"pkg:maven/org.other/log4j@2.14.1",
			false,
		},
		{
			"pkg:deb/debian/libc6",
			"pkg:deb/debian/libc6@2.36-9?distro=debian-12",
			true,
		},
		{
			"not a purl",
			"pkg:npm/x@1",
			false,
		},
	}
	for _, tt := range tests {
		if got := PackageMatches(tt.filter, tt.pkg); got != tt.want {
			t.Errorf("PackageMatches(%q, %q) = %v", tt.filter, tt.pkg, got)
		}
	}
}
