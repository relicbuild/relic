package xray

import (
	"context"
	"slices"
	"time"

	"relic/api/adapters/artifactory/xray/xraymap"
	"relic/internal/access/auth"
	"relic/internal/scanning/scan"
	"relic/internal/scanning/scan/policy"
)

// Violation is a vulnerability that a watch's policy rule matched.
type Violation struct {
	IssueID         string               `json:"issue_id"`
	Type            string               `json:"type"`
	Severity        string               `json:"severity"`
	Summary         string               `json:"summary"`
	WatchName       string               `json:"watch_name"`
	FailBuild       bool                 `json:"fail_build"`
	FailPullRequest bool                 `json:"fail_pull_request"`
	Policies        []ViolationPolicy    `json:"policies"`
	CVEs            []CVE                `json:"cves,omitempty"`
	Components      map[string]Component `json:"components"`
	References      []string             `json:"references"`
	IgnoreURL       string               `json:"ignore_url"`
}

// ViolationPolicy names the policy rule behind a violation.
type ViolationPolicy struct {
	Policy              string `json:"policy"`
	Rule                string `json:"rule"`
	IsBlocking          bool   `json:"is_blocking"`
	IsSkipNotApplicable bool   `json:"is_skip_not_applicable"`

	failBuild bool
}

func violations(
	watches []policy.Watch,
	pols map[string]policy.Policy,
	rules []policy.IgnoreRule,
	vulns []Vulnerability,
	build *buildRef,
) ([]Violation, bool) {
	now := time.Now()
	out := make([]Violation, 0, len(vulns))
	failBuild := false

	for _, v := range vulns {
		is := policy.Issue{
			ID:        v.vulnID,
			Malicious: scan.IsMalicious(v.vulnID),
			Severity:  Severity(v.Severity),
		}
		for _, c := range v.CVEs {
			is.CVEs = append(is.CVEs, c.CVE)
		}

		if build != nil {
			is.Build = build.Name
		}

		for _, c := range v.CVEs {
			is.CVSS = max(is.CVSS, policy.MaxScore(c.CVSSV3Score))
		}

		for _, c := range v.Components {
			is.Fixable = is.Fixable || len(c.FixedVersions) > 0
		}

		byWatch := map[string]*Violation{}

		var order []string

		subject := policy.Subject{
			VulnerabilityIDs: []string{
				v.IssueID,
				v.vulnID,
			},
		}
		for _, c := range v.CVEs {
			if c.CVE != "" {
				subject.CVEs = append(subject.CVEs, c.CVE)
			}
		}

		for id := range v.Components {
			if p, err := xraymap.ToPURL(id); err == nil {
				subject.Packages = append(subject.Packages, p)
			}
		}

		if build != nil {
			subject.BuildName, subject.BuildNumber = build.Name, build.Number
		}

		for _, m := range policy.Evaluate(watches, pols, is) {
			subject.Watch, subject.Policy = m.Watch, m.Policy

			if slices.ContainsFunc(rules, func(r policy.IgnoreRule) bool {
				return r.Ignores(subject, now)
			}) {
				continue
			}

			vi, ok := byWatch[m.Watch]
			if !ok {
				vi = &Violation{
					IssueID:    v.IssueID,
					Type:       string(m.Kind),
					Severity:   v.Severity,
					Summary:    v.Summary,
					WatchName:  m.Watch,
					CVEs:       v.CVEs,
					Components: v.Components,
					References: v.References,
					Policies:   []ViolationPolicy{},
				}
				byWatch[m.Watch] = vi
				order = append(order, m.Watch)
			}

			vi.FailBuild = vi.FailBuild || m.FailBuild
			vi.Policies = append(
				vi.Policies,
				ViolationPolicy{
					Policy:     m.Policy,
					Rule:       m.Rule,
					IsBlocking: m.BlockDownload,
					failBuild:  m.FailBuild,
				},
			)
		}

		for _, name := range order {
			out = append(out, *byWatch[name])
			failBuild = failBuild || byWatch[name].FailBuild
		}
	}

	return out, failBuild
}

func (h *Handler) watchesFor(
	ctx context.Context,
	names []string,
	kind policy.ScopeKind,
	resource string,
) ([]policy.Watch, map[string]policy.Policy, error) {
	all, err := h.loadWatches(ctx)
	if err != nil {
		return nil, nil, err
	}

	var picked []policy.Watch

	for _, w := range all {
		if slices.Contains(names, w.Name) || (kind != "" && w.Covers(kind, resource)) {
			picked = append(picked, w)
		}
	}

	pols, err := h.loadPolicies(ctx)

	return picked, pols, err
}

func hasFailBuildRule(watches []policy.Watch, pols map[string]policy.Policy) bool {
	for _, w := range watches {
		for _, name := range w.Policies {
			for _, r := range pols[name].Rules {
				if r.FailBuild {
					return true
				}
			}
		}
	}

	return false
}

// SetDecisions makes failed build scans record their policy decisions.
func (h *Handler) SetDecisions(d *policy.Decisions) { h.decisions = d }

func (h *Handler) recordBuildDecisions(ctx context.Context, build string, found []Violation) {
	for _, v := range found {
		if !v.FailBuild {
			continue
		}

		for _, p := range v.Policies {
			if !p.failBuild {
				continue
			}

			h.decisions.Record(ctx, policy.Decision{
				Kind:    policy.DecisionBuild,
				Subject: build,
				Policy:  p.Policy,
				Watch:   v.WatchName,
				Rule:    p.Rule,
				Reason:  v.IssueID + " violates policy " + p.Policy + " (watch " + v.WatchName + ")",
				Actor:   auth.FromContext(ctx).Name(),
			})
		}
	}
}
