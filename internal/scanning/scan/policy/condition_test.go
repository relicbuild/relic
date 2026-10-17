package policy

import (
	"errors"
	"testing"

	"relic/internal/scanning/scan"
)

func TestConditions(t *testing.T) {
	t.Parallel()

	critical := Issue{
		ID:         "CVE-2021-44228",
		Severity:   scan.SeverityCritical,
		CVSS:       10,
		Package:    "log4j-core",
		Version:    "2.14.1",
		PURL:       "pkg:maven/org.apache.logging.log4j/log4j-core@2.14.1",
		Repository: "libs-release",
		Path:       "org/app/1.0/app.jar",
	}
	internal := critical
	internal.PURL = "pkg:maven/org.example/tool@1.0"
	advisory := critical
	advisory.ID = "GHSA-jfh8-c2jp-5v3q"
	advisory.CVEs = []string{
		"CVE-2021-44228",
	}

	secretInTest := Issue{
		Analyzer: scan.AnalyzerSecrets,
		Severity: scan.SeverityHigh,
		Rule:     "generic-api-key",
		File:     "src/client_test.go",
	}
	secret := secretInTest
	secret.File = "config/prod.env"

	for name, tt := range map[string]struct {
		rule  Rule
		issue Issue
		want  bool
	}{
		"narrows a severity rule": {
			rule: Rule{
				MinSeverity: scan.SeverityHigh,
				Condition:   `!purl.startsWith("pkg:maven/org.example/")`,
			},
			issue: critical,
			want:  true,
		},
		"narrowed out": {
			rule: Rule{
				MinSeverity: scan.SeverityHigh,
				Condition:   `!purl.startsWith("pkg:maven/org.example/")`,
			},
			issue: internal,
		},
		"alone, on vulnerabilities": {
			rule: Rule{
				Condition: `cvss >= 9.0 && repository == "libs-release"`,
			},
			issue: critical,
			want:  true,
		},
		"alone, on secrets outside tests": {
			rule: Rule{
				Condition: `analyzer == "secrets" && !file.endsWith("_test.go")`,
			},
			issue: secret,
			want:  true,
		},
		"alone, a secret in a test": {
			rule: Rule{
				Condition: `analyzer == "secrets" && !file.endsWith("_test.go")`,
			},
			issue: secretInTest,
		},
		"on the package name and version": {
			rule: Rule{
				Condition: `package_name == "log4j-core" && version.startsWith("2.14")`,
			},
			issue: critical,
			want:  true,
		},
		"on a list": {
			rule: Rule{
				Condition: `"CVE-2021-44228" in cves`,
			},
			issue: advisory,
			want:  true,
		},
		"not on an empty list": {
			rule: Rule{
				Condition: `"CVE-2021-44228" in cves`,
			},
			issue: critical,
			want:  false,
		},
		"an evaluation error fails closed": {
			rule: Rule{
				Condition: `!cves[0].startsWith("CVE-2020")`,
			},
			issue: secret,
			want:  true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tt.rule.Name = "r"

			p := Policy{
				Name: "p",
				Kind: KindSecurity,
				Rules: []Rule{
					tt.rule,
				},
			}
			if err := p.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}

			if got := violates(KindSecurity, tt.rule, tt.issue); got != tt.want {
				t.Errorf("violates = %v, want %v", got, tt.want)
			}
		})
	}

	for name, cond := range map[string]string{
		"not an expression": `severity ==`,
		"not a bool":        `cvss + 1.0`,
		"unknown variable":  `owner == "me"`,
	} {
		for _, p := range []Policy{
			{
				Name: "p",
				Kind: KindSecurity,
				Rules: []Rule{
					{
						Name:      "r",
						Condition: cond,
					},
				},
			},
			{
				Name: "p",
				Kind: KindLicense,
				Rules: []Rule{
					{
						Name: "r",
						BannedLicenses: []string{
							"GPL-3.0-only",
						},
						Condition: cond,
					},
				},
			},
		} {
			if err := p.Validate(); !errors.Is(err, ErrInvalid) {
				t.Errorf("%s, %s policy: Validate = %v", name, p.Kind, err)
			}
		}
	}
}
