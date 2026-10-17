// Package policy evaluates Relic Scan policies and watches.
package policy

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"relic/internal/scanning/scan"
)

// Kind is what a policy's rules look at.
type Kind string

// Policy kinds.
const (
	KindSecurity Kind = "security"
	KindLicense  Kind = "license"
)

// ErrInvalid is returned for malformed policies, watches and ignore rules.
var ErrInvalid = errors.New("invalid policy document")

// Policy is an ordered list of rules that turn findings into violations.
type Policy struct {
	Name        string `json:"name"`
	Kind        Kind   `json:"kind"`
	Description string `json:"description,omitempty"`
	Rules       []Rule `json:"rules"`
}

// Rule is one policy rule: its criteria select findings, its actions say
// what a violation does.
type Rule struct {
	Name     string `json:"name"`
	Priority int    `json:"priority"`

	MinSeverity    scan.Severity `json:"minSeverity,omitempty"`
	CVSS           *CVSSRange    `json:"cvss,omitempty"`
	FixVersionOnly bool          `json:"fixVersionOnly,omitempty"`
	// IgnoreUnreachable skips vulnerabilities govulncheck found no call
	// path to.
	IgnoreUnreachable bool `json:"ignoreUnreachable,omitempty"`
	// Malicious matches malicious-package findings (MAL- advisories) only.
	Malicious bool `json:"malicious,omitempty"`
	// Secrets matches secrets found in content; Misconfigurations
	// misconfigurations, at or above MinSeverity when it is set.
	Secrets           bool `json:"secrets,omitempty"`
	Misconfigurations bool `json:"misconfigurations,omitempty"`
	// LiveSecrets matches secrets their provider still accepts.
	LiveSecrets bool `json:"liveSecrets,omitempty"`
	// EndOfLife matches components past or near end of life, at or above
	// MinSeverity when it is set; Unmaintained deprecated and unmaintained
	// packages.
	EndOfLife    bool `json:"endOfLife,omitempty"`
	Unmaintained bool `json:"unmaintained,omitempty"`
	// Condition is a CEL expression that narrows what the rule matches,
	// or, alone, is what it matches.
	Condition string `json:"condition,omitempty"`

	AllowedLicenses []string `json:"allowedLicenses,omitempty"`
	BannedLicenses  []string `json:"bannedLicenses,omitempty"`
	// AllowUnknown, when set to false, makes findings without a known
	// license violations.
	AllowUnknown *bool `json:"allowUnknown,omitempty"`

	// FailBuild fails build scans with a violation.
	FailBuild bool `json:"failBuild,omitempty"`
	// BlockDownload refuses downloads of violating files.
	BlockDownload bool `json:"blockDownload,omitempty"`
	// BlockUnscanned also refuses downloads of files not scanned yet.
	BlockUnscanned bool `json:"blockUnscanned,omitempty"`
}

// CVSSRange is an inclusive CVSS score range.
type CVSSRange struct {
	From float64 `json:"from"`
	To   float64 `json:"to"`
}

// ScopeKind is what a watch scope covers.
type ScopeKind string

// Scope kinds.
const (
	ScopeRepository ScopeKind = "repository"
	ScopeBuild      ScopeKind = "build"
)

// Watch applies policies to repositories and builds.
type Watch struct {
	Name        string  `json:"name"`
	Description string  `json:"description,omitempty"`
	Active      bool    `json:"active"`
	Scopes      []Scope `json:"scopes"`
	// Policies names the policies the watch applies.
	Policies []string `json:"policies"`
}

// Scope is what a watch covers of one kind of resource: all of them, or
// some by name.
type Scope struct {
	Kind  ScopeKind `json:"kind"`
	All   bool      `json:"all,omitempty"`
	Names []string  `json:"names,omitempty"`
}

// Validate checks a policy.
func (p Policy) Validate() error {
	switch {
	case p.Name == "":
		return fmt.Errorf("%w: name is required", ErrInvalid)
	case p.Kind != KindSecurity && p.Kind != KindLicense:
		return fmt.Errorf("%w: kind must be security or license", ErrInvalid)
	case len(p.Rules) == 0:
		return fmt.Errorf("%w: at least one rule is required", ErrInvalid)
	}

	for _, r := range p.Rules {
		if r.Name == "" {
			return fmt.Errorf("%w: every rule needs a name", ErrInvalid)
		}

		if r.Condition != "" {
			if _, err := compileCondition(r.Condition); err != nil {
				return fmt.Errorf("rule %s: %w", r.Name, err)
			}
		}

		switch p.Kind {
		case KindSecurity:
			severityRule := r.MinSeverity != "" && !r.Misconfigurations && !r.EndOfLife
			n := count(
				severityRule,
				r.CVSS != nil,
				r.Malicious,
				r.Secrets,
				r.Misconfigurations,
				r.EndOfLife,
				r.Unmaintained,
				r.LiveSecrets,
			)

			if n == 0 && r.Condition != "" {
				continue
			}

			if n != 1 {
				return fmt.Errorf(
					"%w: rule %s needs exactly one of a minimum severity, a CVSS range, malicious packages, "+
						"secrets, live secrets, misconfigurations, end of life and unmaintained packages",
					ErrInvalid,
					r.Name,
				)
			}

			if r.MinSeverity != "" && !r.MinSeverity.Known() {
				return fmt.Errorf("%w: rule %s: unknown severity %q", ErrInvalid, r.Name, r.MinSeverity)
			}
		case KindLicense:
			if (len(r.AllowedLicenses) == 0) == (len(r.BannedLicenses) == 0) {
				return fmt.Errorf("%w: rule %s needs exactly one of allowed and banned licenses", ErrInvalid, r.Name)
			}
		}
	}

	return nil
}

// Validate checks a watch.
func (w Watch) Validate() error {
	if w.Name == "" {
		return fmt.Errorf("%w: a watch needs a name", ErrInvalid)
	}

	for _, s := range w.Scopes {
		if s.Kind != ScopeRepository && s.Kind != ScopeBuild {
			return fmt.Errorf("%w: unsupported scope kind %q", ErrInvalid, s.Kind)
		}

		if !s.All && len(s.Names) == 0 {
			return fmt.Errorf("%w: a %s scope needs names or all", ErrInvalid, s.Kind)
		}

		if slices.Contains(s.Names, "") {
			return fmt.Errorf("%w: a %s scope has an empty name", ErrInvalid, s.Kind)
		}
	}

	if slices.Contains(w.Policies, "") {
		return fmt.Errorf("%w: an empty policy name", ErrInvalid)
	}

	return nil
}

// Covers reports whether an active watch covers the named resource.
func (w Watch) Covers(kind ScopeKind, name string) bool {
	if !w.Active {
		return false
	}

	return slices.ContainsFunc(w.Scopes, func(s Scope) bool {
		return s.Kind == kind && (s.All || slices.Contains(s.Names, name))
	})
}

// Issue is a finding to evaluate: a vulnerability (Severity, CVSS; a
// malicious package is one with Malicious set), an analyzer's issue
// (Analyzer: secrets, misconfiguration or model, with Severity), an operational
// risk (Risk, with Severity) or a license (License).
type Issue struct {
	Analyzer string
	// Risk is the kind of operational risk: end_of_life, deprecated or
	// unmaintained.
	Risk string
	// SecretLive is set for a secret its provider still accepts.
	SecretLive bool
	// Reachability is a vulnerability's: reachable, not_reachable or
	// unknown ("" is unknown).
	Reachability               string
	Malicious                  bool
	ID, Package, Version, PURL string
	CVEs                       []string
	Rule, File                 string
	Repository, Path, Build    string
	Severity                   scan.Severity
	CVSS                       float64 // highest v3 score, 0 when unknown
	Fixable                    bool
	License                    string // SPDX id, "" when not a license issue
	IsLicense                  bool
}

// Kinds of operational risk an Issue carries.
const (
	RiskEndOfLife    = "end_of_life"
	RiskDeprecated   = "deprecated"
	RiskUnmaintained = "unmaintained"
)

// Match is a rule an issue violates.
type Match struct {
	Watch, Policy, Rule string
	// Issue names what violates the rule, where a caller sets it.
	Issue         string
	Kind          Kind
	FailBuild     bool
	BlockDownload bool
}

// Evaluate returns the rules the issue violates under the given watches,
// at most one per watch and policy: the first matching rule by priority.
func Evaluate(watches []Watch, policies map[string]Policy, is Issue) []Match {
	var out []Match

	for _, w := range watches {
		if !w.Active {
			continue
		}

		for _, name := range w.Policies {
			p, ok := policies[name]
			if !ok || (p.Kind == KindLicense) != is.IsLicense {
				continue
			}

			rules := slices.Clone(p.Rules)
			slices.SortStableFunc(rules, func(a, b Rule) int {
				return a.Priority - b.Priority
			})

			for _, r := range rules {
				if violates(p.Kind, r, is) {
					out = append(out, Match{
						Watch:         w.Name,
						Policy:        p.Name,
						Rule:          r.Name,
						Kind:          p.Kind,
						FailBuild:     r.FailBuild,
						BlockDownload: r.BlockDownload,
					})

					break
				}
			}
		}
	}

	return out
}

func violates(kind Kind, r Rule, is Issue) bool {
	return matches(kind, r, is) && holds(r.Condition, is)
}

func matches(kind Kind, r Rule, is Issue) bool {
	switch kind {
	case KindSecurity:
		switch {
		case r.Secrets:
			return is.Analyzer == scan.AnalyzerSecrets
		case r.LiveSecrets:
			return is.Analyzer == scan.AnalyzerSecrets && is.SecretLive
		case r.Misconfigurations:
			return is.Analyzer == scan.AnalyzerMisconfiguration &&
				(r.MinSeverity == "" || is.Severity.Rank() >= r.MinSeverity.Rank())
		case r.EndOfLife:
			return is.Risk == RiskEndOfLife && (r.MinSeverity == "" || is.Severity.Rank() >= r.MinSeverity.Rank())
		case r.Unmaintained:
			return is.Risk == RiskDeprecated || is.Risk == RiskUnmaintained
		case r.MinSeverity == "" && r.CVSS == nil && !r.Malicious && r.Condition != "":
			return true
		case (is.Analyzer != "" && is.Analyzer != scan.AnalyzerModel) || is.Risk != "":
			return false
		case r.Malicious:
			return is.Malicious
		}

		if r.FixVersionOnly && !is.Fixable {
			return false
		}

		if r.IgnoreUnreachable && is.Reachability == scan.NotReachable {
			return false
		}

		if r.CVSS != nil {
			return is.CVSS > 0 && is.CVSS >= r.CVSS.From && is.CVSS <= r.CVSS.To
		}

		return is.Severity.Rank() >= r.MinSeverity.Rank() && is.Severity.Rank() > 0
	case KindLicense:
		lic := strings.ToLower(is.License)
		if lic == "" || lic == "unknown" {
			return r.AllowUnknown != nil && !*r.AllowUnknown
		}

		if len(r.BannedLicenses) > 0 {
			return bannedLicense(is.License, r.BannedLicenses)
		}

		return !allowedLicense(is.License, r.AllowedLicenses)
	}

	return false
}

// MaxScore parses CVSS score strings and returns the highest.
func MaxScore(scores ...string) float64 {
	best := 0.0
	for _, s := range scores {
		if v, err := strconv.ParseFloat(s, 64); err == nil && v > best {
			best = v
		}
	}

	return best
}

func count(bs ...bool) int {
	n := 0

	for _, b := range bs {
		if b {
			n++
		}
	}

	return n
}
