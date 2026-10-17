// Package xraypolicy translates the scan API's policy and watch documents
// and ignore rules to and from Relic Scan's policies, watches and ignore
// rules.
package xraypolicy

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"relic/api/adapters/artifactory/xray/xraymap"
	"relic/internal/scanning/scan"
	"relic/internal/scanning/scan/policy"
)

// Policy is a policy document.
type Policy struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
	Rules       []Rule `json:"rules"`
}

// Rule is one policy rule.
type Rule struct {
	Name     string   `json:"name"`
	Priority int      `json:"priority"`
	Criteria Criteria `json:"criteria"`
	Actions  Actions  `json:"actions"`
}

// Criteria select findings.
type Criteria struct {
	MinSeverity     string     `json:"min_severity,omitempty"`
	CVSSRange       *CVSSRange `json:"cvss_range,omitempty"`
	FixVersionOnly  bool       `json:"fix_version_dependant,omitempty"`
	AllowedLicenses []string   `json:"allowed_licenses,omitempty"`
	BannedLicenses  []string   `json:"banned_licenses,omitempty"`
	AllowUnknown    *bool      `json:"allow_unknown,omitempty"`
}

// CVSSRange is an inclusive CVSS score range.
type CVSSRange struct {
	From float64 `json:"from"`
	To   float64 `json:"to"`
}

// Actions are what a violation of the rule does.
type Actions struct {
	FailBuild     bool          `json:"fail_build"`
	BlockDownload BlockDownload `json:"block_download"`
}

// BlockDownload blocks downloads of violating artifacts.
type BlockDownload struct {
	Active    bool `json:"active"`
	Unscanned bool `json:"unscanned"`
}

// Watch is a watch document.
type Watch struct {
	GeneralData      GeneralData      `json:"general_data"`
	ProjectResources ProjectResources `json:"project_resources"`
	AssignedPolicies []AssignedPolicy `json:"assigned_policies"`
}

// GeneralData names a watch.
type GeneralData struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Active      bool   `json:"active"`
}

// ProjectResources lists what a watch covers.
type ProjectResources struct {
	Resources []Resource `json:"resources"`
}

// Resource is a watched resource: "repository" or "build" by name, or
// "all-repos" or "all-builds".
type Resource struct {
	Type     string `json:"type"`
	Name     string `json:"name,omitempty"`
	BinMgrID string `json:"bin_mgr_id,omitempty"`
}

// AssignedPolicy names a policy a watch applies.
type AssignedPolicy struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

const binMgrID = "default"

// IgnoreRule is an ignore rule document.
type IgnoreRule struct {
	ID            string        `json:"id,omitempty"`
	Notes         string        `json:"notes,omitempty"`
	ExpiresAt     *time.Time    `json:"expires_at,omitempty"`
	IgnoreFilters IgnoreFilters `json:"ignore_filters"`
}

// IgnoreFilters narrow an ignore rule.
type IgnoreFilters struct {
	Vulnerabilities []string          `json:"vulnerabilities,omitempty"`
	CVEs            []string          `json:"cves,omitempty"`
	Licenses        []string          `json:"licenses,omitempty"`
	Watches         []string          `json:"watches,omitempty"`
	Policies        []string          `json:"policies,omitempty"`
	Components      []IgnoreComponent `json:"components,omitempty"`
	Artifacts       []IgnoreArtifact  `json:"artifacts,omitempty"`
	Builds          []IgnoreBuild     `json:"builds,omitempty"`
}

var ignoreFilterNames = []string{
	"vulnerabilities",
	"cves",
	"licenses",
	"watches",
	"policies",
	"components",
	"artifacts",
	"builds",
}

// UnmarshalJSON reads ignore filters and refuses, with policy.ErrInvalid, a
// filter Relic does not evaluate: dropping it would ignore more than the
// rule names.
func (f *IgnoreFilters) UnmarshalJSON(b []byte) error {
	var named map[string]json.RawMessage
	if err := json.Unmarshal(b, &named); err != nil {
		return err
	}

	for name, value := range named {
		if !slices.Contains(ignoreFilterNames, name) && !emptyJSON(value) {
			return fmt.Errorf("%w: the ignore filter %s is not supported", policy.ErrInvalid, name)
		}
	}

	type filters IgnoreFilters

	return json.Unmarshal(b, (*filters)(f))
}

func emptyJSON(value json.RawMessage) bool {
	switch strings.TrimSpace(string(value)) {
	case "null", "[]", "{}", `""`:
		return true
	}

	return false
}

// IgnoreComponent is "<type>://<name>" with an optional version.
type IgnoreComponent struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// IgnoreArtifact matches an artifact path ("repo/path/"), as a prefix. Name
// and Version are read only to refuse them.
type IgnoreArtifact struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
	Path    string `json:"path,omitempty"`
}

// IgnoreBuild matches a build, with an optional number.
type IgnoreBuild struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// Severity reads a severity; "Information" is low.
func Severity(s string) scan.Severity {
	if strings.EqualFold(s, "information") {
		return scan.SeverityLow
	}

	return scan.Severity(strings.ToLower(s))
}

// PolicyFromXray reads a policy document.
func PolicyFromXray(p Policy) policy.Policy {
	out := policy.Policy{
		Name:        p.Name,
		Kind:        policy.Kind(strings.ToLower(p.Type)),
		Description: p.Description,
		Rules:       make([]policy.Rule, 0, len(p.Rules)),
	}
	for _, r := range p.Rules {
		c, a := r.Criteria, r.Actions

		rule := policy.Rule{
			Name:            r.Name,
			Priority:        r.Priority,
			FixVersionOnly:  c.FixVersionOnly,
			AllowedLicenses: c.AllowedLicenses,
			BannedLicenses:  c.BannedLicenses,
			AllowUnknown:    c.AllowUnknown,
			FailBuild:       a.FailBuild,
			BlockDownload:   a.BlockDownload.Active,
			BlockUnscanned:  a.BlockDownload.Unscanned,
		}
		if c.MinSeverity != "" {
			rule.MinSeverity = Severity(c.MinSeverity)
		}

		if c.CVSSRange != nil {
			rule.CVSS = &policy.CVSSRange{
				From: c.CVSSRange.From,
				To:   c.CVSSRange.To,
			}
		}

		out.Rules = append(out.Rules, rule)
	}

	return out
}

// KeepRelicCriteria copies onto updated's rules the criteria a policy
// document cannot carry from the stored rules of the same name.
func KeepRelicCriteria(updated, stored policy.Policy) policy.Policy {
	byName := make(map[string]policy.Rule, len(stored.Rules))
	for _, r := range stored.Rules {
		byName[r.Name] = r
	}

	for i, r := range updated.Rules {
		s, ok := byName[r.Name]
		if !ok {
			continue
		}

		r.Condition = s.Condition
		r.IgnoreUnreachable = s.IgnoreUnreachable

		if keepsSelection(r, s) {
			r.Malicious = s.Malicious
			r.Secrets = s.Secrets
			r.LiveSecrets = s.LiveSecrets
			r.Misconfigurations = s.Misconfigurations
			r.EndOfLife = s.EndOfLife
			r.Unmaintained = s.Unmaintained
		}

		updated.Rules[i] = r
	}

	return updated
}

// keepsSelection reports whether updated still selects findings the way
// stored does: it sets no CVSS range, and a minimum severity only where
// stored grades its findings by severity.
func keepsSelection(updated, stored policy.Rule) bool {
	if updated.CVSS != nil {
		return false
	}

	return updated.MinSeverity == "" || stored.Misconfigurations || stored.EndOfLife
}

// PolicyToXray renders a policy as a policy document.
func PolicyToXray(p policy.Policy) Policy {
	out := Policy{
		Name:        p.Name,
		Type:        string(p.Kind),
		Description: p.Description,
		Rules:       make([]Rule, 0, len(p.Rules)),
	}
	for _, r := range p.Rules {
		rule := Rule{
			Name:     r.Name,
			Priority: r.Priority,
			Criteria: Criteria{
				MinSeverity:     xraymap.Severity(string(r.MinSeverity)),
				FixVersionOnly:  r.FixVersionOnly,
				AllowedLicenses: r.AllowedLicenses,
				BannedLicenses:  r.BannedLicenses,
				AllowUnknown:    r.AllowUnknown,
			},
			Actions: Actions{
				FailBuild: r.FailBuild,
				BlockDownload: BlockDownload{
					Active:    r.BlockDownload,
					Unscanned: r.BlockUnscanned,
				},
			},
		}
		if r.MinSeverity == "" {
			rule.Criteria.MinSeverity = ""
		}

		if r.CVSS != nil {
			rule.Criteria.CVSSRange = &CVSSRange{
				From: r.CVSS.From,
				To:   r.CVSS.To,
			}
		}

		out.Rules = append(out.Rules, rule)
	}

	return out
}

// WatchFromXray reads a watch document.
func WatchFromXray(w Watch) policy.Watch {
	out := policy.Watch{
		Name:        w.GeneralData.Name,
		Description: w.GeneralData.Description,
		Active:      w.GeneralData.Active,
		Scopes:      []policy.Scope{},
		Policies:    make([]string, 0, len(w.AssignedPolicies)),
	}
	scope := func(kind policy.ScopeKind) *policy.Scope {
		for i := range out.Scopes {
			if out.Scopes[i].Kind == kind {
				return &out.Scopes[i]
			}
		}

		out.Scopes = append(out.Scopes, policy.Scope{
			Kind: kind,
		})

		return &out.Scopes[len(out.Scopes)-1]
	}

	for _, r := range w.ProjectResources.Resources {
		switch r.Type {
		case "all-repos":
			scope(policy.ScopeRepository).All = true
		case "all-builds":
			scope(policy.ScopeBuild).All = true
		default:
			s := scope(policy.ScopeKind(r.Type))
			s.Names = append(s.Names, r.Name)
		}
	}

	for _, p := range w.AssignedPolicies {
		out.Policies = append(out.Policies, p.Name)
	}

	return out
}

// WatchToXray renders a watch as a watch document.
func WatchToXray(w policy.Watch, kinds map[string]policy.Kind) Watch {
	out := Watch{
		GeneralData: GeneralData{
			Name:        w.Name,
			Description: w.Description,
			Active:      w.Active,
		},
		ProjectResources: ProjectResources{
			Resources: []Resource{},
		},
		AssignedPolicies: make([]AssignedPolicy, 0, len(w.Policies)),
	}
	for _, s := range w.Scopes {
		if s.All {
			typ := "all-repos"
			if s.Kind == policy.ScopeBuild {
				typ = "all-builds"
			}

			out.ProjectResources.Resources = append(
				out.ProjectResources.Resources,
				Resource{
					Type:     typ,
					BinMgrID: binMgrID,
				},
			)
		}

		for _, n := range s.Names {
			out.ProjectResources.Resources = append(
				out.ProjectResources.Resources,
				Resource{
					Type:     string(s.Kind),
					Name:     n,
					BinMgrID: binMgrID,
				},
			)
		}
	}

	for _, name := range w.Policies {
		out.AssignedPolicies = append(out.AssignedPolicies, AssignedPolicy{
			Name: name,
			Type: string(kinds[name]),
		})
	}

	return out
}

// IgnoreRuleFromXray reads an ignore rule document.
func IgnoreRuleFromXray(r IgnoreRule) (policy.IgnoreRule, error) {
	f := r.IgnoreFilters

	out := policy.IgnoreRule{
		ID:              r.ID,
		Notes:           r.Notes,
		Expires:         r.ExpiresAt,
		Vulnerabilities: f.Vulnerabilities,
		CVEs:            f.CVEs,
		Licenses:        f.Licenses,
		Watches:         f.Watches,
		Policies:        f.Policies,
	}
	if out.Expires != nil && out.Expires.IsZero() {
		out.Expires = nil
	}

	for _, c := range f.Components {
		p, err := xraymap.PackageToPURL(c.Name, c.Version)
		if err != nil {
			return policy.IgnoreRule{}, fmt.Errorf("%w: component %s: %w", policy.ErrInvalid, c.Name, err)
		}

		out.Packages = append(out.Packages, p)
	}

	for _, a := range f.Artifacts {
		if a.Name != "" || a.Version != "" {
			return policy.IgnoreRule{}, fmt.Errorf(
				"%w: an ignored artifact is matched by its path only, not by name or version",
				policy.ErrInvalid,
			)
		}

		out.ArtifactPaths = append(out.ArtifactPaths, a.Path)
	}

	for _, b := range f.Builds {
		out.Builds = append(out.Builds, policy.Build{
			Name:   b.Name,
			Number: b.Version,
		})
	}

	return out, nil
}

// IgnoreRuleToXray renders an ignore rule as an ignore rule document.
func IgnoreRuleToXray(r policy.IgnoreRule) IgnoreRule {
	out := IgnoreRule{
		ID:        r.ID,
		Notes:     r.Notes,
		ExpiresAt: r.Expires,
		IgnoreFilters: IgnoreFilters{
			Vulnerabilities: r.Vulnerabilities,
			CVEs:            r.CVEs,
			Licenses:        r.Licenses,
			Watches:         r.Watches,
			Policies:        r.Policies,
		},
	}
	for _, p := range r.Packages {
		name, version, err := xraymap.PackageFromPURL(p)
		if err != nil {
			name, version = p, ""
		}

		out.IgnoreFilters.Components = append(
			out.IgnoreFilters.Components,
			IgnoreComponent{
				Name:    name,
				Version: version,
			},
		)
	}

	for _, a := range r.ArtifactPaths {
		out.IgnoreFilters.Artifacts = append(out.IgnoreFilters.Artifacts, IgnoreArtifact{
			Path: a,
		})
	}

	for _, b := range r.Builds {
		out.IgnoreFilters.Builds = append(out.IgnoreFilters.Builds, IgnoreBuild{
			Name:    b.Name,
			Version: b.Number,
		})
	}

	return out
}

// Watches renders a list of watches.
func Watches(ws []policy.Watch, kinds map[string]policy.Kind) []Watch {
	out := make([]Watch, 0, len(ws))
	for _, w := range ws {
		out = append(out, WatchToXray(w, kinds))
	}

	return out
}

// Policies renders a list of policies.
func Policies(ps []policy.Policy) []Policy {
	out := make([]Policy, 0, len(ps))
	for _, p := range ps {
		out = append(out, PolicyToXray(p))
	}

	return out
}

// IgnoreRules renders a list of ignore rules.
func IgnoreRules(rs []policy.IgnoreRule) []IgnoreRule {
	out := make([]IgnoreRule, 0, len(rs))
	for _, r := range rs {
		out = append(out, IgnoreRuleToXray(r))
	}

	return out
}

// Kinds returns the kind of each policy.
func Kinds(ps []policy.Policy) map[string]policy.Kind {
	out := make(map[string]policy.Kind, len(ps))
	for _, p := range ps {
		out[p.Name] = p.Kind
	}

	return out
}
