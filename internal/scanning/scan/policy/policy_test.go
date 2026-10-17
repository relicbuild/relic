package policy

import (
	"errors"
	"testing"

	"relic/internal/scanning/scan"
)

func TestEvaluate(t *testing.T) {
	t.Parallel()

	no := false
	policies := map[string]Policy{
		"sec": {
			Name: "sec",
			Kind: KindSecurity,
			Rules: []Rule{
				{
					Name:        "critical-fail",
					Priority:    1,
					MinSeverity: scan.SeverityCritical,
					FailBuild:   true,
				},
				{
					Name:        "high-warn",
					Priority:    2,
					MinSeverity: scan.SeverityHigh,
				},
			},
		},
		"cvss": {
			Name: "cvss",
			Kind: KindSecurity,
			Rules: []Rule{
				{
					Name: "7-8",
					CVSS: &CVSSRange{
						From: 7,
						To:   8,
					},
				},
			},
		},
		"fixable": {
			Name: "fixable",
			Kind: KindSecurity,
			Rules: []Rule{
				{
					Name:           "fix",
					MinSeverity:    scan.SeverityLow,
					FixVersionOnly: true,
				},
			},
		},
		"lic": {
			Name: "lic",
			Kind: KindLicense,
			Rules: []Rule{
				{
					Name: "no-gpl",
					BannedLicenses: []string{
						"GPL-3.0",
					},
					FailBuild: true,
				},
			},
		},
		"allow": {
			Name: "allow",
			Kind: KindLicense,
			Rules: []Rule{
				{
					Name: "mit-only",
					AllowedLicenses: []string{
						"MIT",
					},
					AllowUnknown: &no,
				},
			},
		},
		"custom": {
			Name: "custom",
			Kind: KindLicense,
			Rules: []Rule{
				{
					Name: "no-corp",
					BannedLicenses: []string{
						"Corp Proprietary License",
					},
				},
			},
		},
	}
	watch := func(name string, active bool, pols ...string) Watch {
		return Watch{
			Name:     name,
			Active:   active,
			Policies: pols,
		}
	}

	tests := []struct {
		name    string
		watches []Watch
		issue   Issue
		want    []string // "watch/policy/rule"
		fail    bool
	}{
		{
			"critical fails the build",
			[]Watch{
				watch("w", true, "sec"),
			},
			Issue{
				Severity: scan.SeverityCritical,
			},
			[]string{
				"w/sec/critical-fail",
			},
			true,
		},
		{
			"high matches the second rule",
			[]Watch{
				watch("w", true, "sec"),
			},
			Issue{
				Severity: scan.SeverityHigh,
			},
			[]string{
				"w/sec/high-warn",
			},
			false,
		},
		{
			"medium matches nothing",
			[]Watch{
				watch("w", true, "sec"),
			},
			Issue{
				Severity: scan.SeverityMedium,
			},
			nil,
			false,
		},
		{
			"unknown never meets a minimum",
			[]Watch{
				watch("w", true, "fixable"),
			},
			Issue{
				Severity: scan.SeverityUnknown,
				Fixable:  true,
			},
			nil,
			false,
		},
		{
			"inactive watch",
			[]Watch{
				watch("w", false, "sec"),
			},
			Issue{
				Severity: scan.SeverityCritical,
			},
			nil,
			false,
		},
		{
			"cvss in range",
			[]Watch{
				watch("w", true, "cvss"),
			},
			Issue{
				Severity: scan.SeverityHigh,
				CVSS:     7.5,
			},
			[]string{
				"w/cvss/7-8",
			},
			false,
		},
		{
			"cvss out of range",
			[]Watch{
				watch("w", true, "cvss"),
			},
			Issue{
				Severity: scan.SeverityHigh,
				CVSS:     9.8,
			},
			nil,
			false,
		},
		{
			"fix required but none",
			[]Watch{
				watch("w", true, "fixable"),
			},
			Issue{
				Severity: scan.SeverityHigh,
			},
			nil,
			false,
		},
		{
			"banned license",
			[]Watch{
				watch("w", true, "lic"),
			},
			Issue{
				IsLicense: true,
				License:   "gpl-3.0",
			},
			[]string{
				"w/lic/no-gpl",
			},
			true,
		},
		{
			"license policy ignores vulnerabilities",
			[]Watch{
				watch("w", true, "lic"),
			},
			Issue{
				Severity: scan.SeverityCritical,
			},
			nil,
			false,
		},
		{
			"unknown license disallowed",
			[]Watch{
				watch("w", true, "allow"),
			},
			Issue{
				IsLicense: true,
			},
			[]string{
				"w/allow/mit-only",
			},
			false,
		},
		{
			"allowed license",
			[]Watch{
				watch("w", true, "allow"),
			},
			Issue{
				IsLicense: true,
				License:   "MIT",
			},
			nil,
			false,
		},
		{
			"banned license in an AND expression",
			[]Watch{
				watch("w", true, "lic"),
			},
			Issue{
				IsLicense: true,
				License:   "MIT AND GPL-3.0",
			},
			[]string{
				"w/lic/no-gpl",
			},
			true,
		},
		{
			"banned license beside a non-SPDX one",
			[]Watch{
				watch("w", true, "lic"),
			},
			Issue{
				IsLicense: true,
				License:   "GPL-3.0 AND proprietary",
			},
			[]string{
				"w/lic/no-gpl",
			},
			true,
		},
		{
			"banned license with an alternative",
			[]Watch{
				watch("w", true, "lic"),
			},
			Issue{
				IsLicense: true,
				License:   "MIT OR GPL-3.0",
			},
			nil,
			false,
		},
		{
			"banned license under its or-later form",
			[]Watch{
				watch("w", true, "lic"),
			},
			Issue{
				IsLicense: true,
				License:   "GPL-3.0+",
			},
			[]string{
				"w/lic/no-gpl",
			},
			true,
		},
		{
			"allowed license among alternatives",
			[]Watch{
				watch("w", true, "allow"),
			},
			Issue{
				IsLicense: true,
				License:   "(MIT OR Apache-2.0)",
			},
			nil,
			false,
		},
		{
			"allowed license with a required other",
			[]Watch{
				watch("w", true, "allow"),
			},
			Issue{
				IsLicense: true,
				License:   "MIT AND Apache-2.0",
			},
			[]string{
				"w/allow/mit-only",
			},
			false,
		},
		{
			"a name that is not SPDX is compared as it is",
			[]Watch{
				watch("w", true, "custom"),
			},
			Issue{
				IsLicense: true,
				License:   "corp proprietary license",
			},
			[]string{
				"w/custom/no-corp",
			},
			false,
		},
		{
			"another name that is not SPDX",
			[]Watch{
				watch("w", true, "custom"),
			},
			Issue{
				IsLicense: true,
				License:   "Some Other License",
			},
			nil,
			false,
		},
		{
			"two watches",
			[]Watch{
				watch("a", true, "sec"),
				watch("b", true, "sec", "missing"),
			},
			Issue{
				Severity: scan.SeverityCritical,
			},
			[]string{
				"a/sec/critical-fail",
				"b/sec/critical-fail",
			},
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := Evaluate(tt.watches, policies, tt.issue)

			var names []string

			fail := false

			for _, m := range got {
				names = append(names, m.Watch+"/"+m.Policy+"/"+m.Rule)
				fail = fail || m.FailBuild
			}

			if len(names) != len(tt.want) || fail != tt.fail {
				t.Fatalf("Evaluate = %v (fail %v), want %v (fail %v)", names, fail, tt.want, tt.fail)
			}

			for i := range names {
				if names[i] != tt.want[i] {
					t.Errorf("match %d = %s, want %s", i, names[i], tt.want[i])
				}
			}
		})
	}
}

func TestValidateCVSSRange(t *testing.T) {
	t.Parallel()

	for name, tt := range map[string]struct {
		from, to float64
		valid    bool
	}{
		"the whole scale": {
			from:  0,
			to:    10,
			valid: true,
		},
		"one score": {
			from:  7,
			to:    7,
			valid: true,
		},
		"from above to": {
			from: 9,
			to:   2,
		},
		"below zero": {
			from: -5,
			to:   5,
		},
		"above ten": {
			from: 5,
			to:   50,
		},
	} {
		err := Policy{
			Name: "x",
			Kind: KindSecurity,
			Rules: []Rule{
				{
					Name: "r",
					CVSS: &CVSSRange{
						From: tt.from,
						To:   tt.to,
					},
				},
			},
		}.Validate()
		if (err == nil) != tt.valid || (err != nil && !errors.Is(err, ErrInvalid)) {
			t.Errorf("%s: Validate = %v, want valid %v", name, err, tt.valid)
		}
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()

	bad := []Policy{
		{},
		{
			Name: "x",
			Kind: "other",
			Rules: []Rule{
				{
					Name: "r",
				},
			},
		},
		{
			Name: "x",
			Kind: KindSecurity,
		},
		{
			Name: "x",
			Kind: KindSecurity,
			Rules: []Rule{
				{
					Name: "r",
				},
			},
		},
		{
			Name: "x",
			Kind: KindSecurity,
			Rules: []Rule{
				{
					Name:        "r",
					MinSeverity: "severe",
				},
			},
		},
		{
			Name: "x",
			Kind: KindSecurity,
			Rules: []Rule{
				{
					Name:        "r",
					MinSeverity: "High",
				},
			},
		},
		{
			Name: "x",
			Kind: KindLicense,
			Rules: []Rule{
				{
					Name: "r",
				},
			},
		},
	}
	for i, p := range bad {
		if err := p.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("policy %d validated: %v", i, err)
		}
	}

	good := Policy{
		Name: "x",
		Kind: KindSecurity,
		Rules: []Rule{
			{
				Name:        "r",
				MinSeverity: scan.SeverityHigh,
			},
		},
	}
	if err := good.Validate(); err != nil {
		t.Error(err)
	}

	w := Watch{
		Name:   "w",
		Active: true,
		Scopes: []Scope{
			{
				Kind: ScopeBuild,
				Names: []string{
					"app",
				},
			},
			{
				Kind: ScopeRepository,
				All:  true,
			},
		},
	}
	if err := w.Validate(); err != nil {
		t.Error(err)
	}

	if !w.Covers(ScopeBuild, "app") || w.Covers(ScopeBuild, "other") || !w.Covers(ScopeRepository, "anything") {
		t.Error("Covers wrong")
	}

	for name, bad := range map[string]Watch{
		"unknown kind": {
			Name: "w",
			Scopes: []Scope{
				{
					Kind: "cluster",
					All:  true,
				},
			},
		},
		"neither names nor all": {
			Name: "w",
			Scopes: []Scope{
				{
					Kind: ScopeBuild,
				},
			},
		},
	} {
		if err := bad.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s validated: %v", name, err)
		}
	}
}

func TestEvaluateMalicious(t *testing.T) {
	t.Parallel()

	policies := map[string]Policy{
		"mal": {
			Name: "mal",
			Kind: KindSecurity,
			Rules: []Rule{
				{
					Name:          "block-malicious",
					Malicious:     true,
					BlockDownload: true,
				},
			},
		},
	}
	watches := []Watch{
		{
			Name:   "w",
			Active: true,
			Policies: []string{
				"mal",
			},
		},
	}

	for name, tt := range map[string]struct {
		issue Issue
		want  int
	}{
		"malicious": {
			issue: Issue{
				Malicious: true,
				Severity:  scan.SeverityCritical,
			},
			want: 1,
		},
		"critical vulnerability": {
			issue: Issue{
				Severity: scan.SeverityCritical,
				CVSS:     9.8,
			},
			want: 0,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := Evaluate(
				watches,
				policies,
				tt.issue,
			); len(got) != tt.want ||
				(tt.want == 1 && !got[0].BlockDownload) {
				t.Errorf("Evaluate = %+v", got)
			}
		})
	}

	for name, r := range map[string]Rule{
		"malicious and a severity": {
			Name:        "r",
			Malicious:   true,
			MinSeverity: scan.SeverityHigh,
		},
		"nothing": {
			Name: "r",
		},
	} {
		p := Policy{
			Name: "p",
			Kind: KindSecurity,
			Rules: []Rule{
				r,
			},
		}
		if err := p.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: Validate = %v", name, err)
		}
	}

	if err := policies["mal"].Validate(); err != nil {
		t.Errorf("a malicious-packages rule: %v", err)
	}
}

func TestEvaluateAnalyzerIssues(t *testing.T) {
	t.Parallel()

	policies := map[string]Policy{
		"p": {
			Name: "p",
			Kind: KindSecurity,
			Rules: []Rule{
				{
					Name:      "secrets",
					Priority:  1,
					Secrets:   true,
					FailBuild: true,
				},
				{
					Name:              "misconfig-high",
					Priority:          2,
					Misconfigurations: true,
					MinSeverity:       scan.SeverityHigh,
				},
				{
					Name:        "vuln-low",
					Priority:    3,
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
				"p",
			},
		},
	}

	for name, tt := range map[string]struct {
		issue Issue
		want  string
	}{
		"a secret": {
			issue: Issue{
				Analyzer: scan.AnalyzerSecrets,
				Severity: scan.SeverityHigh,
			},
			want: "secrets",
		},
		"a high misconfiguration": {
			issue: Issue{
				Analyzer: scan.AnalyzerMisconfiguration,
				Severity: scan.SeverityHigh,
			},
			want: "misconfig-high",
		},
		"a low misconfiguration": {
			issue: Issue{
				Analyzer: scan.AnalyzerMisconfiguration,
				Severity: scan.SeverityLow,
			},
		},
		"a vulnerability": {
			issue: Issue{
				Severity: scan.SeverityMedium,
			},
			want: "vuln-low",
		},
		"a malicious model": {
			issue: Issue{
				Analyzer: scan.AnalyzerModel,
				Severity: scan.SeverityCritical,
				Rule:     "pickle-unsafe-import",
			},
			want: "vuln-low",
		},
		"a model issue of no known severity": {
			issue: Issue{
				Analyzer: scan.AnalyzerModel,
				Severity: scan.SeverityUnknown,
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := Evaluate(watches, policies, tt.issue)
			if (tt.want == "" && len(got) != 0) || (tt.want != "" && (len(got) != 1 || got[0].Rule != tt.want)) {
				t.Errorf("Evaluate = %+v, want rule %q", got, tt.want)
			}
		})
	}

	if err := policies["p"].Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}

	bad := Policy{
		Name: "bad",
		Kind: KindSecurity,
		Rules: []Rule{
			{
				Name:              "both",
				Secrets:           true,
				Misconfigurations: true,
			},
		},
	}
	if err := bad.Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("secrets and misconfigurations in one rule: %v", err)
	}
}

func TestEvaluateOperationalRisks(t *testing.T) {
	t.Parallel()

	policies := map[string]Policy{
		"p": {
			Name: "p",
			Kind: KindSecurity,
			Rules: []Rule{
				{
					Name:        "past-end-of-life",
					Priority:    1,
					EndOfLife:   true,
					MinSeverity: scan.SeverityHigh,
				},
				{
					Name:         "unmaintained",
					Priority:     2,
					Unmaintained: true,
				},
				{
					Name:        "vuln-low",
					Priority:    3,
					MinSeverity: scan.SeverityLow,
				},
			},
		},
		"cel": {
			Name: "cel",
			Kind: KindSecurity,
			Rules: []Rule{
				{
					Name:      "eol-django",
					Condition: `eol && purl.startsWith("pkg:pypi/django@")`,
				},
			},
		},
	}
	watches := []Watch{
		{
			Name:   "w",
			Active: true,
			Policies: []string{
				"p",
			},
		},
	}

	for name, tt := range map[string]struct {
		issue Issue
		want  string
	}{
		"past end of life": {
			issue: Issue{
				Risk:     RiskEndOfLife,
				Severity: scan.SeverityHigh,
			},
			want: "past-end-of-life",
		},
		"end of life soon, below the rule's severity, and no vulnerability rule takes it": {
			issue: Issue{
				Risk:     RiskEndOfLife,
				Severity: scan.SeverityLow,
			},
		},
		"deprecated": {
			issue: Issue{
				Risk:     RiskDeprecated,
				Severity: scan.SeverityMedium,
			},
			want: "unmaintained",
		},
		"unmaintained": {
			issue: Issue{
				Risk:     RiskUnmaintained,
				Severity: scan.SeverityMedium,
			},
			want: "unmaintained",
		},
		"a vulnerability is not a risk": {
			issue: Issue{
				Severity: scan.SeverityHigh,
			},
			want: "vuln-low",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := Evaluate(watches, policies, tt.issue)
			if (tt.want == "" && len(got) != 0) || (tt.want != "" && (len(got) != 1 || got[0].Rule != tt.want)) {
				t.Errorf("Evaluate = %+v, want rule %q", got, tt.want)
			}
		})
	}

	celWatch := []Watch{
		{
			Name:   "w",
			Active: true,
			Policies: []string{
				"cel",
			},
		},
	}

	if got := Evaluate(celWatch, policies, Issue{
		Risk: RiskEndOfLife,
		PURL: "pkg:pypi/django@4.2.1",
	}); len(got) != 1 {
		t.Errorf("eol condition: %+v", got)
	}

	if got := Evaluate(celWatch, policies, Issue{
		PURL: "pkg:pypi/django@4.2.1",
	}); len(got) != 0 {
		t.Errorf("eol condition on a vulnerability: %+v", got)
	}

	for _, name := range []string{
		"p",
		"cel",
	} {
		if err := policies[name].Validate(); err != nil {
			t.Errorf("Validate %s: %v", name, err)
		}
	}

	bad := Policy{
		Name: "bad",
		Kind: KindSecurity,
		Rules: []Rule{
			{
				Name:         "both",
				EndOfLife:    true,
				Unmaintained: true,
			},
		},
	}
	if err := bad.Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("end of life and unmaintained in one rule: %v", err)
	}
}

func TestEvaluateReachability(t *testing.T) {
	t.Parallel()

	policies := map[string]Policy{
		"reachable-high": {
			Name: "reachable-high",
			Kind: KindSecurity,
			Rules: []Rule{
				{
					Name:              "high",
					MinSeverity:       scan.SeverityHigh,
					IgnoreUnreachable: true,
				},
			},
		},
		"cel": {
			Name: "cel",
			Kind: KindSecurity,
			Rules: []Rule{
				{
					Name:      "called",
					Condition: `reachable == "reachable"`,
				},
			},
		},
	}
	watch := func(policy string) []Watch {
		return []Watch{
			{
				Name:   "w",
				Active: true,
				Policies: []string{
					policy,
				},
			},
		}
	}

	for name, tt := range map[string]struct {
		policy       string
		reachability string
		want         bool
	}{
		"reachable": {
			"reachable-high",
			scan.Reachable,
			true,
		},
		"not reachable is skipped": {
			"reachable-high",
			scan.NotReachable,
			false,
		},
		"unknown stays a violation": {
			"reachable-high",
			scan.ReachabilityUnknown,
			true,
		},
		"unset is unknown": {
			"reachable-high",
			"",
			true,
		},
		"a condition on reachable": {
			"cel",
			scan.Reachable,
			true,
		},
		"a condition on an unknown finding": {
			"cel",
			"",
			false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := Evaluate(watch(tt.policy), policies, Issue{
				Severity:     scan.SeverityCritical,
				Reachability: tt.reachability,
			})
			if (len(got) == 1) != tt.want {
				t.Errorf("Evaluate = %+v, want a violation: %v", got, tt.want)
			}
		})
	}
}

func TestEvaluateLiveSecrets(t *testing.T) {
	t.Parallel()

	policies := map[string]Policy{
		"p": {
			Name: "p",
			Kind: KindSecurity,
			Rules: []Rule{
				{
					Name:        "live",
					LiveSecrets: true,
				},
			},
		},
	}
	watches := []Watch{
		{
			Name:   "w",
			Active: true,
			Policies: []string{
				"p",
			},
		},
	}

	if got := Evaluate(watches, policies, Issue{
		Analyzer:   scan.AnalyzerSecrets,
		SecretLive: true,
	}); len(got) != 1 {
		t.Errorf("a live secret = %+v", got)
	}

	if got := Evaluate(watches, policies, Issue{
		Analyzer: scan.AnalyzerSecrets,
	}); len(got) != 0 {
		t.Errorf("an unchecked secret = %+v", got)
	}

	if err := policies["p"].Validate(); err != nil {
		t.Error(err)
	}

	cel := Rule{
		Name:      "c",
		Condition: `analyzer == "secrets" && secret_live`,
	}
	if !violates(KindSecurity, cel, Issue{
		Analyzer:   scan.AnalyzerSecrets,
		SecretLive: true,
	}) {
		t.Error("secret_live is not set in conditions")
	}
}

func TestPolicyLoads(t *testing.T) {
	t.Parallel()

	for name, tt := range map[string]struct {
		policy Policy
		want   previewLoads
	}{
		"license": {
			policy: Policy{
				Kind: KindLicense,
				Rules: []Rule{
					{
						BannedLicenses: []string{
							"GPL-3.0-only",
						},
					},
				},
			},
			want: previewLoads{
				licenses: true,
			},
		},
		"severity": {
			policy: Policy{
				Kind: KindSecurity,
				Rules: []Rule{
					{
						MinSeverity: scan.SeverityHigh,
					},
				},
			},
			want: previewLoads{
				findings: true,
				issues:   true,
			},
		},
		"severity of reachable code": {
			policy: Policy{
				Kind: KindSecurity,
				Rules: []Rule{
					{
						MinSeverity:       scan.SeverityHigh,
						IgnoreUnreachable: true,
					},
				},
			},
			want: previewLoads{
				findings:     true,
				reachability: true,
				issues:       true,
			},
		},
		"secrets and end of life": {
			policy: Policy{
				Kind: KindSecurity,
				Rules: []Rule{
					{
						Secrets: true,
					},
					{
						EndOfLife: true,
					},
				},
			},
			want: previewLoads{
				issues: true,
				risks:  true,
			},
		},
		"condition alone": {
			policy: Policy{
				Kind: KindSecurity,
				Rules: []Rule{
					{
						Condition: `repository == "libs"`,
					},
				},
			},
			want: previewLoads{
				findings:     true,
				reachability: true,
				issues:       true,
				risks:        true,
			},
		},
	} {
		if got := policyLoads(tt.policy); got != tt.want {
			t.Errorf("%s: loads = %+v, want %+v", name, got, tt.want)
		}
	}
}
