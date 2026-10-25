package relicapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect/v2"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/access/auth"
	"relic/internal/delivery/buildinfo"
	"relic/internal/scanning/scan"
	"relic/internal/scanning/scan/policy"
)

func buildRun(ctx context.Context, d Dependencies, project, name, number string) (buildinfo.Document, error) {
	ok, err := (&buildServer{
		d: d,
	}).canRead(ctx, project, name)
	if err != nil {
		return buildinfo.Document{}, err
	}

	if !ok {
		return buildinfo.Document{}, buildinfo.ErrNotFound
	}

	run, err := d.Builds.Run(ctx, project, name, number)
	if err != nil {
		return buildinfo.Document{}, err
	}

	return run.Document, nil
}

func graphScanAvailable(ctx context.Context, d Dependencies) error {
	if d.Scans == nil || d.QueueGraphScan == nil || d.GraphScanWorkers == nil {
		return connect.NewError(connect.CodeUnavailable, "scanning is not available")
	}

	running, err := d.GraphScanWorkers(ctx)
	if err != nil {
		return fmt.Errorf("graph scan workers: %w", err)
	}

	if !running {
		return connect.NewError(connect.CodeUnavailable, "scanning is not available: no worker runs Relic Scan")
	}

	return nil
}

func buildScanRequest(ctx context.Context, d Dependencies, doc buildinfo.Document) (scan.GraphRequest, error) {
	var req scan.GraphRequest

	addContent := func(sha256, sha1 string) error {
		sha256 = strings.ToLower(sha256)
		if sha256 == "" && sha1 != "" {
			var err error
			if sha256, err = d.Repositories.SHA256OfSHA1(ctx, sha1); err != nil {
				return err
			}
		}

		if sha256 != "" && !slices.Contains(req.Artifacts, sha256) {
			req.Artifacts = append(req.Artifacts, sha256)
		}

		return nil
	}

	for _, m := range doc.Modules {
		for _, dep := range m.Dependencies {
			p, ok := scan.DependencyPURL(m.Type, dep.ID)
			if !ok {
				if err := addContent(dep.SHA256, dep.SHA1); err != nil {
					return scan.GraphRequest{}, err
				}

				continue
			}

			if !slices.Contains(req.PURLs, p) {
				req.PURLs = append(req.PURLs, p)
			}
		}

		for _, a := range m.Artifacts {
			if err := addContent(a.SHA256, a.SHA1); err != nil {
				return scan.GraphRequest{}, err
			}
		}
	}

	return req, nil
}

func (s *scanServer) ScanBuildRun(
	ctx context.Context,
	req *relicv1.ScanBuildRunRequest,
) (*relicv1.ScanBuildRunResponse, error) {
	m := req

	doc, err := buildRun(ctx, s.d, m.GetProject(), m.GetName(), m.GetNumber())
	if err != nil {
		return nil, err
	}

	if err := graphScanAvailable(ctx, s.d); err != nil {
		return nil, err
	}

	graph, err := buildScanRequest(ctx, s.d, doc)
	if err != nil {
		return nil, err
	}

	id, err := s.d.Scans.QueueGraph(
		ctx,
		auth.FromContext(ctx).Name(),
		graph,
		&scan.GraphBuild{
			Name:    doc.Name,
			Number:  doc.Number,
			Project: m.GetProject(),
		},
	)
	if err != nil {
		return nil, err
	}

	if err := s.d.QueueGraphScan(ctx, id); err != nil {
		return nil, errors.Join(err, s.d.Scans.FailGraph(context.WithoutCancel(ctx), id, err))
	}

	return &relicv1.ScanBuildRunResponse{}, nil
}

func (s *scanServer) GetBuildRunScan(
	ctx context.Context,
	req *relicv1.GetBuildRunScanRequest,
) (*relicv1.GetBuildRunScanResponse, error) {
	m := req

	if _, err := buildRun(ctx, s.d, m.GetProject(), m.GetName(), m.GetNumber()); err != nil {
		return nil, err
	}

	g, err := s.d.ScanResults.LatestBuildGraphScan(ctx, scan.GraphBuild{
		Name:    m.GetName(),
		Number:  m.GetNumber(),
		Project: m.GetProject(),
	})
	if err != nil {
		return nil, err
	}

	res, err := s.graphResult(ctx, g, func(w policy.Watch) bool {
		return w.Covers(policy.ScopeBuild, m.GetName())
	}, policy.Subject{
		BuildName:   m.GetName(),
		BuildNumber: m.GetNumber(),
	})
	if err != nil {
		return nil, err
	}

	for _, v := range res.violations {
		if v.GetFailBuild() {
			s.d.PolicyDecisions.Record(ctx, policy.Decision{
				Kind:    policy.DecisionBuild,
				Subject: m.GetName() + "/" + m.GetNumber(),
				Policy:  v.GetPolicy(),
				Watch:   v.GetWatch(),
				Rule:    v.GetRule(),
				Reason:  v.GetFindingId() + " in " + v.GetPurl() + " violates policy " + v.GetPolicy(),
				Actor:   auth.FromContext(ctx).Name(),
			})
		}
	}

	return &relicv1.GetBuildRunScanResponse{
		Scan:       res.scan,
		Violations: res.violations,
		FailBuild:  res.failBuild,
	}, nil
}

type graphResult struct {
	scan                     *relicv1.ItemScan
	violations               []*relicv1.PolicyViolation
	failBuild, blockDownload bool
}

func (s *scanServer) graphResult(
	ctx context.Context,
	g scan.GraphScan,
	pick func(policy.Watch) bool,
	subject policy.Subject,
) (graphResult, error) {
	res := graphResult{
		scan: &relicv1.ItemScan{
			Status: scanStatusMessage(g.Status),
			Reason: g.Error,
			Counts: map[string]int32{},
		},
	}
	if g.Status != scan.StatusDone {
		return res, nil
	}

	var rep scan.Report
	if err := json.Unmarshal(g.Result, &rep); err != nil {
		return res, err
	}

	findings := slices.DeleteFunc(rep.Findings, func(f scan.Finding) bool {
		return !slices.Contains(g.Request.PURLs, f.Package.PURL)
	})

	stored, err := s.d.ScanResults.FindingsFor(ctx, g.Request.Artifacts)
	if err != nil {
		return res, err
	}

	for _, f := range stored {
		findings = append(findings, f.Finding)
	}

	findings = uniqueFindings(findings)
	res.scan.VulnerabilityDatabaseBuilt = tsValue(rep.DBBuilt)
	res.scan.Components = int32(len(g.Request.PURLs)) //nolint:gosec // a component count

	for _, f := range findings {
		res.scan.Findings = append(res.scan.Findings, findingMessage(f))
		res.scan.Counts[string(f.Severity)]++
	}

	slices.SortStableFunc(res.scan.Findings, func(a, b *relicv1.Finding) int {
		return int(a.GetSeverity()) - int(b.GetSeverity())
	})

	res.violations, err = s.violations(ctx, pick, subject, findings)
	if err != nil {
		return res, err
	}

	for _, v := range res.violations {
		res.failBuild = res.failBuild || v.GetFailBuild()
		res.blockDownload = res.blockDownload || v.GetBlockDownload()
	}

	return res, nil
}

func uniqueFindings(fs []scan.Finding) []scan.Finding {
	seen := map[[2]string]bool{}

	return slices.DeleteFunc(fs, func(f scan.Finding) bool {
		k := [2]string{
			f.ID,
			f.Package.PURL,
		}
		if seen[k] {
			return true
		}

		seen[k] = true

		return false
	})
}

func (s *scanServer) violations(
	ctx context.Context,
	pick func(policy.Watch) bool,
	subject policy.Subject,
	findings []scan.Finding,
) ([]*relicv1.PolicyViolation, error) {
	watches, err := s.d.ScanPolicies.Watches(ctx)
	if err != nil {
		return nil, err
	}

	watches = slices.DeleteFunc(watches, func(w policy.Watch) bool {
		return !pick(w)
	})
	if len(watches) == 0 {
		return nil, nil
	}

	pols, err := s.d.ScanPolicies.PolicyMap(ctx)
	if err != nil {
		return nil, err
	}

	ignores, err := s.d.ScanPolicies.IgnoreRules(ctx, true)
	if err != nil {
		return nil, err
	}

	now := time.Now()

	var out []*relicv1.PolicyViolation

	for _, f := range findings {
		is := policy.Issue{
			ID:        f.ID,
			CVEs:      f.CVEs(),
			Package:   f.Package.Name,
			Version:   f.Package.Version,
			PURL:      f.Package.PURL,
			Build:     subject.BuildName,
			Malicious: scan.IsMalicious(f.ID),
			Severity:  f.Severity,
			Fixable:   len(f.FixedVersions) > 0,
		}
		for _, c := range f.CVSS {
			if strings.HasPrefix(c.Version, "3") {
				is.CVSS = max(is.CVSS, c.Score)
			}
		}

		subject.VulnerabilityIDs = []string{
			f.ID,
		}
		subject.CVEs = f.CVEs()
		subject.Packages = []string{
			f.Package.PURL,
		}

		for _, match := range policy.Evaluate(watches, pols, is) {
			subject.Watch, subject.Policy = match.Watch, match.Policy

			if slices.ContainsFunc(ignores, func(r policy.IgnoreRule) bool {
				return r.Ignores(subject, now)
			}) {
				continue
			}

			out = append(out, &relicv1.PolicyViolation{
				Watch:         match.Watch,
				Policy:        match.Policy,
				Rule:          match.Rule,
				FindingId:     f.ID,
				Purl:          f.Package.PURL,
				FailBuild:     match.FailBuild,
				BlockDownload: match.BlockDownload,
			})
		}
	}

	return out, nil
}
