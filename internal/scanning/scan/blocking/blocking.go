// Package blocking enforces download blocking.
package blocking

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"relic/internal/access/audit"
	"relic/internal/access/auth"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/database"
	"relic/internal/platform/events"
	"relic/internal/platform/telemetry"
	"relic/internal/scanning/scan"
	"relic/internal/scanning/scan/policy"
)

const cacheTTL = 30 * time.Second

var heldFile = regexp.MustCompile(`(?i)` + scan.PackagePattern)

// Blocker decides whether a download is blocked.
type Blocker struct {
	db          *database.DB
	now         func() time.Time
	audit       audit.Recorder
	decisions   *policy.Decisions
	events      events.Publisher
	quarantined QuarantinedRepositories
	registry    RegistryApprovals

	mu       sync.Mutex
	loaded   time.Time
	watches  []policy.Watch
	policies map[string]policy.Policy
	ignores  []policy.IgnoreRule

	components componentCache
}

// New returns a Blocker.
func New(db *database.DB) *Blocker {
	return &Blocker{
		db:  db,
		now: time.Now,
	}
}

// SetAudit makes the blocker record every download it blocks.
func (b *Blocker) SetAudit(r audit.Recorder) { b.audit = r }

func (d Decision) cause() string {
	switch {
	case d.Policy == "quarantine":
		return "quarantine"
	case d.Rule == "unscanned":
		return "unscanned"
	}

	return "policy"
}

// SetEvents publishes blocked downloads and quarantine decisions to p.
func (b *Blocker) SetEvents(p events.Publisher) { b.events = p }

// SetDecisions makes the blocker record every download it blocks as a
// policy decision.
func (b *Blocker) SetDecisions(d *policy.Decisions) { b.decisions = d }

// Decision explains a block.
type Decision struct {
	Blocked bool
	Reason  string
	// Policy, Watch and Rule name what blocked: a policy's rule, "quarantine"
	// with its state, or a watch's rule for unscanned files.
	Policy string
	Watch  string
	Rule   string
}

// RegistryApprovals tells which items are registry entries no one approved.
type RegistryApprovals interface {
	Unapproved(ctx context.Context, repoKey, itemPath, sha256Hex string) (bool, error)
}

// SetRegistryApprovals refuses downloads of unapproved MCP server and agent
// skill registry entries, except to administrators and viewers, who review
// them.
func (b *Blocker) SetRegistryApprovals(r RegistryApprovals) { b.registry = r }

// Explain decides for content sha256Hex, stored at itemPath in repoKey,
// without recording the decision.
func (b *Blocker) Explain(ctx context.Context, repoKey, itemPath, sha256Hex string) (Decision, error) {
	if p := auth.FromContext(ctx); b.registry != nil && !p.Admin && !p.Viewer {
		unapproved, err := b.registry.Unapproved(ctx, repoKey, itemPath, sha256Hex)
		if err != nil {
			return Decision{}, err
		}

		if unapproved {
			return Decision{
				Blocked: true,
				Reason:  "the registry entry is not approved",
				Rule:    "registry-approval",
			}, nil
		}
	}

	held, err := b.quarantine(ctx, repoKey, itemPath, sha256Hex)
	if err != nil || held.Blocked {
		return held, err
	}

	d, err := b.decide(ctx, repoKey, itemPath, sha256Hex)
	if err == nil && !d.Blocked && d.Rule == "" {
		d = held
	}

	return d, err
}

// Check decides for content sha256Hex, stored at itemPath in repoKey, and
// records a block in the audit log, the decisions and the events.
func (b *Blocker) Check(ctx context.Context, repoKey, itemPath, sha256Hex string) (Decision, error) {
	d, err := b.Explain(ctx, repoKey, itemPath, sha256Hex)
	if err == nil && d.Rule == RuleNotHeld {
		telemetry.DownloadNotHeld(d.cause())
	}

	if err != nil || !d.Blocked {
		return d, err
	}

	telemetry.DownloadBlocked(d.cause())

	target := repoKey + "/" + strings.TrimPrefix(itemPath, "/")
	actor := auth.FromContext(ctx).Name()

	if b.audit != nil {
		b.audit.Record(ctx, audit.Event{
			Actor:   actor,
			Action:  "download.blocked",
			Target:  target,
			Outcome: audit.Denied,
			Detail:  d.Reason,
		})
	}

	events.Publish(ctx, b.events, events.Event{
		Type:    events.DownloadBlocked,
		Subject: target,
		Actor:   actor,
		Data: map[string]any{
			"repository": repoKey,
			"path":       strings.TrimPrefix(itemPath, "/"),
			"sha256":     sha256Hex,
			"reason":     d.Reason,
			"policy":     d.Policy,
			"watch":      d.Watch,
		},
	})

	b.decisions.Record(ctx, policy.Decision{
		Kind:    policy.DecisionDownload,
		Subject: target,
		Policy:  d.Policy,
		Watch:   d.Watch,
		Rule:    d.Rule,
		Reason:  d.Reason,
		Actor:   actor,
	})

	return d, err
}

// RefuseCopy refuses to copy or move file when a download of it would be
// refused (repository.CopyInput.RefuseFile).
func (b *Blocker) RefuseCopy(ctx context.Context, file repository.Item) error {
	d, err := b.Check(ctx, file.Repository, file.Path(), file.Checksums.SHA256)
	if err != nil {
		return err
	}

	if d.Blocked {
		return fmt.Errorf(
			"%w: %s/%s cannot be downloaded, so it cannot be copied or moved: %s",
			repository.ErrForbidden,
			file.Repository,
			file.Path(),
			d.Reason,
		)
	}

	return nil
}

// RuleNotHeld is the rule of a decision that serves a file quarantine or blockUnscanned would hold if it were a
// package, image or model file.
const RuleNotHeld = "not_held"

const notHeldReason = "served without a successful scan: Relic Scan holds only package, image and model files " +
	"until they are scanned"

var unscannedReasons = map[string]string{
	"":                 "the artifact has not been scanned yet",
	scan.StatusQueued:  "the artifact has not been scanned yet",
	scan.StatusFailed:  "the artifact's scan failed",
	scan.StatusSkipped: "Relic Scan skipped the artifact",
}

func (b *Blocker) decide(ctx context.Context, repoKey, itemPath, sha256Hex string) (Decision, error) {
	fileName := path.Base(itemPath)

	watches, pols, ignores, err := b.load(ctx)
	if err != nil {
		return Decision{}, err
	}

	var covering []policy.Watch

	unscanned := false

	for _, w := range watches {
		if !w.Covers(policy.ScopeRepository, repoKey) {
			continue
		}

		blocks := false

		for _, name := range w.Policies {
			for _, r := range pols[name].Rules {
				blocks = blocks || r.BlockDownload
				unscanned = unscanned || (r.BlockDownload && r.BlockUnscanned)
			}
		}

		if blocks {
			covering = append(covering, w)
		}
	}

	if len(covering) == 0 {
		return Decision{}, nil
	}

	a, err := b.db.GetScanStatus(ctx, sha256Hex)

	notScanned := errors.Is(database.NotFound(err), database.ErrNotFound) ||
		(err == nil && a.Status == scan.StatusQueued)
	if err != nil && !notScanned {
		return Decision{}, fmt.Errorf("load scan status: %w", err)
	}

	scanned := !notScanned && (a.Status == scan.StatusDone || a.Status == scan.StatusPartial)
	if !scanned && unscanned && heldFile.MatchString(fileName) {
		return Decision{
			Blocked: true,
			Reason:  unscannedReasons[a.Status],
			Watch:   covering[0].Name,
			Rule:    "unscanned",
		}, nil
	}

	var note Decision
	if !scanned && unscanned {
		note = Decision{
			Reason: notHeldReason,
			Watch:  covering[0].Name,
			Rule:   RuleNotHeld,
		}
	}

	if notScanned {
		return note, nil
	}

	findings, err := b.db.ListFindings(ctx, sha256Hex)
	if err != nil {
		return Decision{}, fmt.Errorf("load findings: %w", err)
	}

	reachability, err := scan.NewResults(b.db).Reachability(ctx, []string{
		sha256Hex,
	})
	if err != nil {
		return Decision{}, err
	}

	for _, f := range findings {
		is := policy.FindingIssue(f, repoKey, itemPath)
		is.Reachability, _ = reachability.State(sha256Hex, f.VulnID, f.Cves, f.Purl)

		subject := policy.Subject{
			VulnerabilityIDs: []string{
				f.VulnID,
			},
			CVEs: f.Cves,
			Packages: []string{
				f.Purl,
			},
			ArtifactPath: repoKey + "/" + itemPath,
		}
		for _, m := range policy.Evaluate(covering, pols, is) {
			subject.Watch, subject.Policy = m.Watch, m.Policy

			if slices.ContainsFunc(ignores, func(r policy.IgnoreRule) bool {
				return r.Ignores(subject, b.now())
			}) {
				continue
			}

			if m.BlockDownload {
				return Decision{
					Blocked: true,
					Reason: fmt.Sprintf(
						"%s in %s violates policy %s (watch %s)",
						f.VulnID,
						f.PackageName,
						m.Policy,
						m.Watch,
					),
					Policy: m.Policy,
					Watch:  m.Watch,
					Rule:   m.Rule,
				}, nil
			}
		}
	}

	issues, err := b.db.ListIssues(ctx, sha256Hex)
	if err != nil {
		return Decision{}, fmt.Errorf("load issues: %w", err)
	}

	for _, i := range issues {
		subject := policy.Subject{
			VulnerabilityIDs: []string{
				i.Rule,
			},
			ArtifactPath: repoKey + "/" + itemPath,
		}

		for _, m := range policy.Evaluate(covering, pols, policy.Issue{
			Analyzer:   i.Analyzer,
			Severity:   scan.Severity(i.Severity),
			Rule:       i.Rule,
			File:       i.File,
			SecretLive: i.Validation == scan.SecretLive,
			Repository: repoKey,
			Path:       itemPath,
		}) {
			subject.Watch, subject.Policy = m.Watch, m.Policy

			if !m.BlockDownload || slices.ContainsFunc(ignores, func(r policy.IgnoreRule) bool {
				return r.Ignores(subject, b.now())
			}) {
				continue
			}

			return Decision{
				Blocked: true,
				Reason: fmt.Sprintf(
					"%s %s in %s violates policy %s (watch %s)",
					i.Analyzer,
					i.Rule,
					i.File,
					m.Policy,
					m.Watch,
				),
				Policy: m.Policy,
				Watch:  m.Watch,
				Rule:   m.Rule,
			}, nil
		}
	}

	risks, err := b.db.ListOperationalRisks(ctx, sha256Hex)
	if err != nil {
		return Decision{}, fmt.Errorf("load operational risks: %w", err)
	}

	for _, r := range risks {
		subject := policy.Subject{
			VulnerabilityIDs: []string{
				r.Kind,
			},
			Packages: []string{
				r.Purl,
			},
			ArtifactPath: repoKey + "/" + itemPath,
		}

		for _, m := range policy.Evaluate(covering, pols, policy.RiskIssue(r, repoKey, itemPath)) {
			subject.Watch, subject.Policy = m.Watch, m.Policy

			if !m.BlockDownload || slices.ContainsFunc(ignores, func(i policy.IgnoreRule) bool {
				return i.Ignores(subject, b.now())
			}) {
				continue
			}

			return Decision{
				Blocked: true,
				Reason: fmt.Sprintf(
					"%s %s: %s violates policy %s (watch %s)",
					r.PackageName,
					r.PackageVersion,
					r.Detail,
					m.Policy,
					m.Watch,
				),
				Policy: m.Policy,
				Watch:  m.Watch,
				Rule:   m.Rule,
			}, nil
		}
	}

	d, err := b.checkLicenses(ctx, covering, pols, ignores, repoKey+"/"+itemPath, a, issues)
	if err == nil && !d.Blocked {
		d = note
	}

	return d, err
}

func (b *Blocker) checkLicenses(
	ctx context.Context,
	covering []policy.Watch,
	pols map[string]policy.Policy,
	ignores []policy.IgnoreRule,
	artifactPath string,
	status database.GetScanStatusRow,
	issues []database.ScanIssue,
) (Decision, error) {
	if !slices.ContainsFunc(covering, func(w policy.Watch) bool {
		return slices.ContainsFunc(w.Policies, func(name string) bool {
			return pols[name].Kind == policy.KindLicense
		})
	}) {
		return Decision{}, nil
	}

	comps, err := b.components.load(ctx, b.db, status.Sha256, status.Updated)
	if err != nil {
		return Decision{}, err
	}

	for _, i := range issues {
		if i.Analyzer == scan.AnalyzerLicense {
			comps = append(comps, scan.Component{
				Name: i.File,
				Licenses: []string{
					i.Rule,
				},
				LicenseIDs: scan.LicenseIDs(i.Rule),
			})
		}
	}

	subject := policy.Subject{
		ArtifactPath: artifactPath,
	}
	for _, v := range policy.LicenseViolations(covering, pols, ignores, subject, comps, b.now()) {
		if !v.BlockDownload {
			continue
		}

		license := v.License
		if license == "" {
			license = "an unknown license"
		}

		return Decision{
			Blocked: true,
			Reason: fmt.Sprintf(
				"%s under %s violates policy %s (watch %s)",
				comps[v.Component].Name,
				license,
				v.Policy,
				v.Watch,
			),
			Policy: v.Policy,
			Watch:  v.Watch,
			Rule:   v.Rule,
		}, nil
	}

	return Decision{}, nil
}

// Invalidate forgets cached watches and policies.
func (b *Blocker) Invalidate() {
	b.mu.Lock()
	b.loaded = time.Time{}
	b.mu.Unlock()
}

func (b *Blocker) load(ctx context.Context) ([]policy.Watch, map[string]policy.Policy, []policy.IgnoreRule, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.loaded.IsZero() && b.now().Sub(b.loaded) < cacheTTL {
		return b.watches, b.policies, b.ignores, nil
	}

	wrows, err := b.db.ListScanWatches(ctx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load watches: %w", err)
	}

	prows, err := b.db.ListScanPolicies(ctx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load policies: %w", err)
	}

	irows, err := b.db.ListActiveIgnoreRules(ctx, b.now())
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load ignore rules: %w", err)
	}

	b.ignores = nil

	for _, r := range irows {
		var rule policy.IgnoreRule
		if json.Unmarshal(r.Doc, &rule) == nil {
			b.ignores = append(b.ignores, rule)
		}
	}

	b.watches = nil // callers may still hold the previous slice

	for _, r := range wrows {
		var w policy.Watch
		if json.Unmarshal(r.Doc, &w) == nil {
			b.watches = append(b.watches, w)
		}
	}

	b.policies = map[string]policy.Policy{}

	for _, r := range prows {
		var p policy.Policy
		if json.Unmarshal(r.Doc, &p) == nil {
			b.policies[p.Name] = p
		}
	}

	b.loaded = b.now()

	return b.watches, b.policies, b.ignores, nil
}
