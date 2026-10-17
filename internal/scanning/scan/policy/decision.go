package policy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path"
	"slices"
	"strings"
	"time"

	"relic/internal/platform/database"
	"relic/internal/scanning/endoflife"
	"relic/internal/scanning/scan"
)

// DecisionKind is what a policy decision blocked.
type DecisionKind string

// Decision kinds.
const (
	DecisionDownload DecisionKind = "download"
	DecisionBuild    DecisionKind = "build"
	DecisionRelease  DecisionKind = "release"
)

// Decision is a policy decision that blocked a download, a build or a
// promotion, with how often it was taken.
type Decision struct {
	Kind DecisionKind
	// Subject is what was blocked: <repository>/<path>, <build>/<number> or
	// <release>/<version>.
	Subject string
	// Policy names the policy, the quarantine or the promotion gate.
	Policy string
	Watch  string
	// Rule names the policy's rule, or the environment of a promotion.
	Rule      string
	Reason    string
	Actor     string
	Count     int64
	FirstSeen time.Time
	LastSeen  time.Time
}

// Decisions records and lists policy decisions.
type Decisions struct {
	DB  *database.DB
	Log *slog.Logger
}

// Record stores a decision, or counts it again; a failure is logged, never
// returned, so a block is never undone by its record.
func (d *Decisions) Record(ctx context.Context, dec Decision) {
	if d == nil {
		return
	}

	if err := d.DB.RecordPolicyDecision(ctx, database.RecordPolicyDecisionParams{
		Kind:    string(dec.Kind),
		Subject: dec.Subject,
		Policy:  dec.Policy,
		Watch:   dec.Watch,
		Rule:    dec.Rule,
		Reason:  dec.Reason,
		Actor:   dec.Actor,
		Now:     time.Now().UTC(),
	}); err != nil && d.Log != nil {
		d.Log.WarnContext(ctx, "record policy decision", "kind", dec.Kind, "subject", dec.Subject, "error", err)
	}
}

// List returns the decisions of kind, or of every kind for "", the latest
// first.
func (d *Decisions) List(ctx context.Context, kind DecisionKind, limit int) ([]Decision, error) {
	rows, err := d.DB.ListPolicyDecisions(ctx, database.ListPolicyDecisionsParams{
		Kind:    string(kind),
		MaxRows: int32(min(max(limit, 1), 10_000)), //nolint:gosec // bounded
	})
	if err != nil {
		return nil, fmt.Errorf("list policy decisions: %w", err)
	}

	out := make([]Decision, 0, len(rows))
	for _, r := range rows {
		out = append(out, Decision{
			Kind:      DecisionKind(r.Kind),
			Subject:   r.Subject,
			Policy:    r.Policy,
			Watch:     r.Watch,
			Rule:      r.Rule,
			Reason:    r.Reason,
			Actor:     r.Actor,
			Count:     r.Count,
			FirstSeen: r.FirstSeen,
			LastSeen:  r.LastSeen,
		})
	}

	return out, nil
}

// FindingIssue is the issue a stored finding raises in a file.
func FindingIssue(f database.ScanFinding, repository, path string) Issue {
	is := Issue{
		Malicious:  scan.IsMalicious(f.VulnID),
		Severity:   scan.Severity(f.Severity),
		Fixable:    len(f.FixedVersions) > 0,
		ID:         f.VulnID,
		CVEs:       f.Cves,
		Package:    f.PackageName,
		Version:    f.PackageVersion,
		PURL:       f.Purl,
		Repository: repository,
		Path:       path,
	}

	var cvss []scan.CVSS

	_ = json.Unmarshal(f.Cvss, &cvss)
	for _, c := range cvss {
		if len(c.Version) > 0 && c.Version[0] == '3' {
			is.CVSS = max(is.CVSS, c.Score)
		}
	}

	return is
}

// RiskIssue is the issue a stored operational risk raises in a file.
func RiskIssue(r database.ScanOperationalRisk, repository, path string) Issue {
	return Issue{
		Risk:       r.Kind,
		Severity:   scan.Severity(r.Severity),
		ID:         r.Kind,
		Package:    r.PackageName,
		Version:    r.PackageVersion,
		PURL:       r.Purl,
		Repository: repository,
		Path:       path,
	}
}

// PreviewHit is a stored file a policy would raise a violation on.
type PreviewHit struct {
	Repository string
	Path       string
	SHA256     string
	Rule       string
	// Issue names what violates the rule: a vulnerability, an analyzer's
	// rule or a license, with its package.
	Issue         string
	FailBuild     bool
	BlockDownload bool
}

const (
	previewPage     = 500
	maxPreviewFiles = 50_000
)

type previewLoads struct {
	findings     bool
	reachability bool
	issues       bool
	risks        bool
	licenses     bool
}

func policyLoads(p Policy) previewLoads {
	if p.Kind == KindLicense {
		return previewLoads{
			licenses: true,
		}
	}

	var loads previewLoads

	for _, r := range p.Rules {
		conditional := r.Condition != ""
		analyzerRule := r.Secrets || r.LiveSecrets || r.Misconfigurations
		riskRule := r.EndOfLife || r.Unmaintained

		loads.issues = loads.issues || analyzerRule || conditional
		loads.risks = loads.risks || riskRule || conditional
		findingRule := !analyzerRule && !riskRule

		loads.findings = loads.findings || findingRule
		loads.reachability = loads.reachability || (findingRule && (r.IgnoreUnreachable || conditional))
	}

	return loads
}

// Preview lists, without storing p, the scanned files whose stored findings
// would violate p, up to limit hits. A file is named by its first location
// within repositories, or anywhere when repositories is empty.
func (a *Admin) Preview(ctx context.Context, p Policy, repositories []string, limit int) ([]PreviewHit, bool, error) {
	if err := p.Validate(); err != nil {
		return nil, false, err
	}

	watch := Watch{
		Name:   "preview",
		Active: true,
		Scopes: []Scope{
			{
				Kind:  ScopeRepository,
				All:   len(repositories) == 0,
				Names: repositories,
			},
		},
		Policies: []string{
			p.Name,
		},
	}
	watches := []Watch{
		watch,
	}
	pols := map[string]Policy{
		p.Name: p,
	}

	var (
		hits  []PreviewHit
		after string
		seen  int
	)

	loads := policyLoads(p)

	ignores, err := a.IgnoreRules(ctx, true)
	if err != nil {
		return nil, false, err
	}

	for seen < maxPreviewFiles {
		rows, err := a.DB.ListScannedContents(ctx, database.ListScannedContentsParams{
			Repositories: append([]string{}, repositories...),
			After:        after,
			MaxRows:      previewPage,
		})
		if err != nil {
			return nil, false, fmt.Errorf("list scanned files: %w", err)
		}

		for _, r := range rows {
			after = r.Sha256
			seen++

			if !watch.Covers(ScopeRepository, r.RepoKey) {
				continue
			}

			found, err := a.previewFile(ctx, watches, pols, ignores, loads, r)
			if err != nil {
				return nil, false, err
			}

			hits = append(hits, found...)
			if len(hits) >= limit {
				return hits[:limit], true, nil
			}
		}

		if len(rows) < previewPage {
			break
		}
	}

	return hits, seen >= maxPreviewFiles, nil
}

func (a *Admin) previewFile(
	ctx context.Context,
	watches []Watch,
	pols map[string]Policy,
	ignores []IgnoreRule,
	loads previewLoads,
	r database.ListScannedContentsRow,
) ([]PreviewHit, error) {
	itemPath := strings.TrimPrefix(path.Join(r.Path, r.Name), "/")
	artifactPath := r.RepoKey + "/" + itemPath
	now := time.Now()

	ignored := func(s Subject, m Match) bool {
		s.Watch, s.Policy, s.ArtifactPath = m.Watch, m.Policy, artifactPath

		return slices.ContainsFunc(ignores, func(rule IgnoreRule) bool {
			return rule.Ignores(s, now)
		})
	}

	hit := func(m Match, issue string) PreviewHit {
		return PreviewHit{
			Repository:    r.RepoKey,
			Path:          itemPath,
			SHA256:        r.Sha256,
			Rule:          m.Rule,
			Issue:         issue,
			FailBuild:     m.FailBuild,
			BlockDownload: m.BlockDownload,
		}
	}

	var out []PreviewHit

	findings, err := a.previewFindings(ctx, loads, r.Sha256)
	if err != nil {
		return nil, err
	}

	reachability := scan.ReachabilityIndex{}
	if loads.reachability {
		if reachability, err = scan.NewResults(a.DB).Reachability(ctx, []string{
			r.Sha256,
		}); err != nil {
			return nil, err
		}
	}

	for _, f := range findings {
		is := FindingIssue(f, r.RepoKey, itemPath)
		is.Reachability, _ = reachability.State(r.Sha256, f.VulnID, f.Cves, f.Purl)

		subject := Subject{
			VulnerabilityIDs: []string{
				f.VulnID,
			},
			CVEs: f.Cves,
			Packages: []string{
				f.Purl,
			},
		}

		for _, m := range Evaluate(watches, pols, is) {
			if !ignored(subject, m) {
				out = append(out, hit(m, f.VulnID+" in "+f.Purl))
			}
		}
	}

	issues, err := a.previewIssues(ctx, loads, r.Sha256)
	if err != nil {
		return nil, err
	}

	for _, i := range issues {
		for _, m := range Evaluate(watches, pols, Issue{
			Analyzer:   i.Analyzer,
			Severity:   scan.Severity(i.Severity),
			Rule:       i.Rule,
			File:       i.File,
			SecretLive: i.Validation == scan.SecretLive,
			Repository: r.RepoKey,
			Path:       itemPath,
		}) {
			if !ignored(Subject{
				VulnerabilityIDs: []string{
					i.Rule,
				},
			}, m) {
				out = append(out, hit(m, i.Analyzer+" "+i.Rule+" in "+i.File))
			}
		}
	}

	risks, err := a.previewRisks(ctx, loads, r.Sha256)
	if err != nil {
		return nil, err
	}

	for _, risk := range risks {
		for _, m := range Evaluate(watches, pols, RiskIssue(risk, r.RepoKey, itemPath)) {
			if !ignored(Subject{
				VulnerabilityIDs: []string{
					risk.Kind,
				},
				Packages: []string{
					risk.Purl,
				},
			}, m) {
				out = append(out, hit(m, risk.Kind+" "+risk.Purl))
			}
		}
	}

	if !loads.licenses {
		return out, nil
	}

	comps, err := scan.Components(r.Sbom)
	if err != nil || len(comps) == 0 {
		return out, nil //nolint:nilerr // a file without a readable SBOM has no license findings
	}

	subject := Subject{
		ArtifactPath: artifactPath,
	}
	for _, v := range LicenseViolations(watches, pols, ignores, subject, comps, now) {
		out = append(out, PreviewHit{
			Repository:    r.RepoKey,
			Path:          itemPath,
			SHA256:        r.Sha256,
			Rule:          v.Rule,
			Issue:         v.License + " in " + comps[v.Component].PURL,
			FailBuild:     v.FailBuild,
			BlockDownload: v.BlockDownload,
		})
	}

	return out, nil
}

func (a *Admin) previewFindings(
	ctx context.Context,
	loads previewLoads,
	sha256Hex string,
) ([]database.ScanFinding, error) {
	if !loads.findings {
		return nil, nil
	}

	findings, err := a.DB.ListFindings(ctx, sha256Hex)
	if err != nil {
		return nil, fmt.Errorf("load findings: %w", err)
	}

	return findings, nil
}

func (a *Admin) previewIssues(ctx context.Context, loads previewLoads, sha256Hex string) ([]database.ScanIssue, error) {
	if !loads.issues {
		return nil, nil
	}

	issues, err := a.DB.ListIssues(ctx, sha256Hex)
	if err != nil {
		return nil, fmt.Errorf("load issues: %w", err)
	}

	return issues, nil
}

func (a *Admin) previewRisks(
	ctx context.Context,
	loads previewLoads,
	sha256Hex string,
) ([]database.ScanOperationalRisk, error) {
	if !loads.risks {
		return nil, nil
	}

	risks, err := a.DB.ListOperationalRisks(ctx, sha256Hex)
	if err != nil {
		return nil, fmt.Errorf("load operational risks: %w", err)
	}

	return risks, nil
}

// PackageViolations are the blocking rules (failing builds or blocking
// downloads) that stored findings of the package purl, or its operational
// risks, violate, under the active watches covering repository, or all
// active watches for "".
func (a *Admin) PackageViolations(
	ctx context.Context,
	purl, repository string,
	risks []endoflife.Risk,
) ([]Match, error) {
	watches, err := a.Watches(ctx)
	if err != nil {
		return nil, err
	}

	if repository != "" {
		watches = slices.DeleteFunc(watches, func(w Watch) bool {
			return !w.Covers(ScopeRepository, repository)
		})
	}

	if len(watches) == 0 {
		return nil, nil
	}

	pols, err := a.PolicyMap(ctx)
	if err != nil {
		return nil, err
	}

	ignores, err := a.IgnoreRules(ctx, true)
	if err != nil {
		return nil, err
	}

	findings, err := a.DB.ListFindingsForPackage(ctx, purl)
	if err != nil {
		return nil, fmt.Errorf("list findings of %s: %w", purl, err)
	}

	now := time.Now()
	seen := map[string]bool{}

	var out []Match

	for _, f := range findings {
		subject := Subject{
			VulnerabilityIDs: []string{
				f.VulnID,
			},
			CVEs: f.Cves,
			Packages: []string{
				f.Purl,
			},
		}

		for _, m := range Evaluate(watches, pols, FindingIssue(f, repository, "")) {
			subject.Watch, subject.Policy = m.Watch, m.Policy

			if !m.BlockDownload && !m.FailBuild {
				continue
			}

			if slices.ContainsFunc(ignores, func(r IgnoreRule) bool {
				return r.Ignores(subject, now)
			}) {
				continue
			}

			key := f.VulnID + "|" + m.Watch + "|" + m.Policy + "|" + m.Rule
			if seen[key] {
				continue
			}

			seen[key] = true
			m.Issue = f.VulnID
			out = append(out, m)
		}
	}

	for _, r := range risks {
		subject := Subject{
			Packages: []string{
				r.PURL,
			},
		}

		for _, m := range Evaluate(watches, pols, Issue{
			Risk:       r.Kind,
			Severity:   scan.Severity(r.Severity),
			ID:         r.Kind,
			Package:    r.Name,
			Version:    r.Version,
			PURL:       r.PURL,
			Repository: repository,
		}) {
			subject.Watch, subject.Policy = m.Watch, m.Policy

			if !m.BlockDownload && !m.FailBuild || slices.ContainsFunc(ignores, func(rule IgnoreRule) bool {
				return rule.Ignores(subject, now)
			}) {
				continue
			}

			m.Issue = r.Detail
			out = append(out, m)
		}
	}

	return out, nil
}
