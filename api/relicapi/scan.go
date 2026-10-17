package relicapi

import (
	"context"
	"errors"
	"path"
	"slices"
	"strings"
	"time"
	"uuid"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/access/auth"
	"relic/internal/scanning/cvecatalog"
	"relic/internal/scanning/scan"
	"relic/internal/scanning/scan/policy"
)

type scanServer struct {
	d Dependencies
}

func severityOf(s relicv1.Severity) scan.Severity {
	v, _ := toDomain[scan.Severity](s)
	return v
}

func severityMessage(s scan.Severity) relicv1.Severity { return fromDomain[relicv1.Severity](s) }

const imageManifest = "manifest.json"

func findingMessage(f scan.Finding) *relicv1.Finding {
	out := &relicv1.Finding{
		Id:             f.ID,
		Aliases:        f.Aliases,
		Severity:       severityMessage(f.Severity),
		Summary:        f.Summary,
		PackageName:    f.Package.Name,
		PackageVersion: f.Package.Version,
		Purl:           f.Package.PURL,
		FixedVersions:  f.FixedVersions,
		Urls:           f.URLs,
		Malicious:      scan.IsMalicious(f.ID),
		Reachability:   scan.ReachabilityUnknown,
	}
	for _, c := range f.CVSS {
		out.Cvss = append(out.Cvss, &relicv1.Cvss{
			Version: c.Version,
			Vector:  c.Vector,
			Score:   c.Score,
		})
	}

	return out
}

func (s *scanServer) GetItemScan(
	ctx context.Context,
	req *connect.Request[relicv1.GetItemScanRequest],
) (*connect.Response[relicv1.GetItemScanResponse], error) {
	key, p := req.Msg.GetRepository(), cleanPath(req.Msg.GetPath())
	if err := can(ctx, s.d, key, p, auth.ActionRead); err != nil {
		return nil, err
	}

	it, err := s.d.Repositories.Stat(ctx, key, p)
	if err != nil {
		return nil, err
	}

	out, err := itemScan(ctx, s.d, it)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.GetItemScanResponse{
		Scan: out,
	}), nil
}

func (s *scanServer) ExplainDownload(
	ctx context.Context,
	req *connect.Request[relicv1.ExplainDownloadRequest],
) (*connect.Response[relicv1.ExplainDownloadResponse], error) {
	key, p := req.Msg.GetRepository(), cleanPath(req.Msg.GetPath())
	if err := can(ctx, s.d, key, p, auth.ActionRead); err != nil {
		return nil, err
	}

	it, err := s.d.Repositories.Stat(ctx, key, p)
	if err != nil {
		return nil, err
	}

	if it.IsFolder() {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New(key+"/"+p+" is a folder"))
	}

	out := &relicv1.ExplainDownloadResponse{}
	if s.d.DownloadBlocker == nil {
		return connect.NewResponse(out), nil
	}

	d, err := s.d.DownloadBlocker.Explain(ctx, key, it.Path(), it.Checksums.SHA256)
	if err != nil {
		return nil, err
	}

	out.Blocked, out.Reason, out.Policy, out.Watch, out.Rule = d.Blocked, d.Reason, d.Policy, d.Watch, d.Rule

	return connect.NewResponse(out), nil
}

func page(p, size int32) (int, int) {
	if size <= 0 {
		size = 50
	}

	return int(max(p, 1)), int(min(size, 200))
}

func cveIDs(id string, aliases []string) []string {
	var out []string

	for _, x := range append([]string{
		id,
	}, aliases...) {
		x = strings.ToUpper(x)
		if strings.HasPrefix(x, "CVE-") && !slices.Contains(out, x) {
			out = append(out, x)
		}
	}

	return out
}

func (s *scanServer) ListFindings(
	ctx context.Context,
	req *connect.Request[relicv1.ListFindingsRequest],
) (*connect.Response[relicv1.ListFindingsResponse], error) {
	n, size := page(req.Msg.GetPage(), req.Msg.GetPageSize())

	rows, total, err := s.d.ScanResults.FindingsPage(ctx, scan.FindingsQuery{
		Severity:      severityOf(req.Msg.GetSeverity()),
		Query:         req.Msg.GetQuery(),
		Repository:    req.Msg.GetRepository(),
		PurlType:      req.Msg.GetPurlType(),
		FixableOnly:   req.Msg.GetFixableOnly(),
		KEVOnly:       req.Msg.GetKevOnly(),
		MaliciousOnly: req.Msg.GetMaliciousOnly(),
		Skip:          (n - 1) * size,
		Max:           size,
	})
	if err != nil {
		return nil, err
	}

	var ids []string
	for _, f := range rows {
		ids = append(ids, cveIDs(f.VulnID, f.CVEs)...)
	}

	facts, err := s.d.CVEs.FactsFor(ctx, ids)
	if err != nil {
		return nil, err
	}

	shas := make([]string, 0, len(rows))
	for _, f := range rows {
		shas = append(shas, f.Sha256)
	}

	reachability, err := s.d.ScanResults.Reachability(ctx, shas)
	if err != nil {
		return nil, err
	}

	out := &relicv1.ListFindingsResponse{
		Total:    total,
		Findings: make([]*relicv1.FindingOccurrence, 0, len(rows)),
	}
	for _, f := range rows {
		m := &relicv1.FindingOccurrence{
			Id:             f.VulnID,
			Severity:       severityMessage(f.Severity),
			PackageName:    f.PackageName,
			PackageVersion: f.PackageVersion,
			Summary:        f.Summary,
			Cves:           f.CVEs,
			FixedVersions:  f.FixedVersions,
			Repository:     f.Repository,
			Path:           strings.TrimPrefix(path.Join(f.Path, f.Name), "./"),
			Malicious:      scan.IsMalicious(f.VulnID),
		}
		m.Reachability, _ = reachability.State(f.Sha256, f.VulnID, f.CVEs, f.PURL)

		for _, id := range cveIDs(f.VulnID, f.CVEs) {
			c, ok := facts[id]
			if !ok {
				continue
			}

			if c.EPSS != nil && (m.Epss == nil || *c.EPSS > m.GetEpss()) {
				m.Epss = c.EPSS
			}

			m.Kev = m.Kev || c.KEVAdded != nil
		}

		out.Findings = append(out.Findings, m)
	}

	return connect.NewResponse(out), nil
}

func severityOfRank(rank int32) relicv1.Severity {
	if rank >= 0 && int(rank) < len(scan.Severities) {
		return severityMessage(scan.Severities[rank])
	}

	return relicv1.Severity_SEVERITY_UNKNOWN
}

func (s *scanServer) ListCves(
	ctx context.Context,
	req *connect.Request[relicv1.ListCvesRequest],
) (*connect.Response[relicv1.ListCvesResponse], error) {
	m := req.Msg
	n, size := page(m.GetPage(), m.GetPageSize())

	//nolint:gosec // a small index
	rows, total, err := s.d.CVEs.Page(ctx, cvecatalog.Filter{
		Query:        strings.TrimSpace(m.GetQuery()),
		KEVOnly:      m.GetKevOnly(),
		MinEPSS:      m.GetMinEpss(),
		SeverityRank: int32(slices.Index(scan.Severities, severityOf(m.GetSeverity()))),
		Sort:         m.GetSort(),
		Skip:         (n - 1) * size,
		Max:          size,
	})
	if err != nil {
		return nil, err
	}

	feeds, err := s.d.CVEs.Feeds(ctx)
	if err != nil {
		return nil, err
	}

	out := &relicv1.ListCvesResponse{
		Total: total,
		Cves:  make([]*relicv1.CveSummary, 0, len(rows)),
	}
	for _, c := range rows {
		out.Cves = append(out.Cves, &relicv1.CveSummary{
			Id:             c.ID,
			Title:          c.Title,
			Severity:       severityOfRank(c.SeverityRank),
			Published:      ts(c.Published),
			CvssScore:      c.CVSSScore,
			CvssVersion:    c.CVSSVersion,
			Epss:           c.EPSS,
			EpssPercentile: c.EPSSPercentile,
			Kev:            c.KEVAdded != nil,
			Ransomware:     c.KEVRansomware,
			Exploitation:   c.SSVCExploitation,
			Artifacts:      c.Artifacts,
			Packages:       c.Packages,
		})
	}

	for _, f := range feeds {
		out.Feeds = append(
			out.Feeds,
			&relicv1.CveFeed{
				Name:          f.Name,
				Version:       f.Version,
				LastFetchedAt: timestamppb.New(f.Fetched),
			},
		)
	}

	return connect.NewResponse(out), nil
}

func (s *scanServer) GetCve(
	ctx context.Context,
	req *connect.Request[relicv1.GetCveRequest],
) (*connect.Response[relicv1.GetCveResponse], error) {
	id := strings.ToUpper(req.Msg.GetId())

	c, err := s.d.CVEs.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	affected, err := s.d.CVEs.Affected(ctx, id)
	if err != nil {
		return nil, err
	}

	rank := len(scan.Severities) - 1
	files, packages := map[string]bool{}, map[string]bool{}

	out := &relicv1.Cve{
		Description:     c.Description,
		State:           c.State,
		Updated:         ts(c.Updated),
		Cwes:            c.CWEs,
		References:      c.References,
		CvssVector:      c.CVSSVector,
		CvssSource:      c.CVSSSource,
		Automatable:     c.SSVCAutomatable,
		TechnicalImpact: c.SSVCImpact,
		EpssDate:        ts(c.EPSSDate),
		KevAdded:        ts(c.KEVAdded),
		KevDue:          ts(c.KEVDue),
		KevAction:       c.KEVAction,
		RecordChecked:   ts(c.RecordChecked),
	}
	for _, f := range affected {
		out.Affected = append(out.Affected, &relicv1.AffectedFile{
			Finding:        f.VulnID,
			Severity:       severityMessage(scan.Severity(f.Severity)),
			PackageName:    f.PackageName,
			PackageVersion: f.PackageVersion,
			Purl:           f.PURL,
			FixedVersions:  f.FixedVersions,
			Repository:     f.Repository,
			Path:           strings.TrimPrefix(path.Join(f.Path, f.Name), "./"),
		})
		if i := slices.Index(scan.Severities, scan.Severity(f.Severity)); i >= 0 && i < rank {
			rank = i
		}

		files[f.Sha256], packages[f.PURL] = true, true
	}

	out.Summary = &relicv1.CveSummary{
		Id:             c.ID,
		Title:          c.Title,
		Severity:       severityMessage(scan.Severities[rank]),
		Published:      ts(c.Published),
		CvssScore:      c.CVSSScore,
		CvssVersion:    c.CVSSVersion,
		Epss:           c.EPSS,
		EpssPercentile: c.EPSSPercentile,
		Kev:            c.KEVAdded != nil,
		Ransomware:     c.KEVRansomware,
		Exploitation:   c.SSVCExploitation,
		Artifacts:      int64(len(files)),
		Packages:       int64(len(packages)),
	}

	return connect.NewResponse(&relicv1.GetCveResponse{
		Cve: out,
	}), nil
}

func policyMessage(p policy.Policy) *relicv1.Policy {
	out := &relicv1.Policy{
		Name:        p.Name,
		Kind:        fromDomain[relicv1.PolicyKind](p.Kind),
		Description: p.Description,
	}
	for _, r := range p.Rules {
		m := &relicv1.PolicyRule{
			Name:              r.Name,
			Priority:          int32(r.Priority), //nolint:gosec // a rule priority
			MinSeverity:       severityMessage(r.MinSeverity),
			FixVersionOnly:    r.FixVersionOnly,
			AllowedLicenses:   r.AllowedLicenses,
			BannedLicenses:    r.BannedLicenses,
			AllowUnknown:      r.AllowUnknown,
			FailBuild:         r.FailBuild,
			BlockDownload:     r.BlockDownload,
			BlockUnscanned:    r.BlockUnscanned,
			Malicious:         r.Malicious,
			Secrets:           r.Secrets,
			Misconfigurations: r.Misconfigurations,
			Condition:         r.Condition,
			EndOfLife:         r.EndOfLife,
			Unmaintained:      r.Unmaintained,
			IgnoreUnreachable: r.IgnoreUnreachable,
			LiveSecrets:       r.LiveSecrets,
		}
		if r.CVSS != nil {
			m.CvssFrom, m.CvssTo = &r.CVSS.From, &r.CVSS.To
		}

		out.Rules = append(out.Rules, m)
	}

	return out
}

func policyOf(m *relicv1.Policy) policy.Policy {
	kind, _ := toDomain[policy.Kind](m.GetKind())

	out := policy.Policy{
		Name:        m.GetName(),
		Kind:        kind,
		Description: m.GetDescription(),
	}
	for _, r := range m.GetRules() {
		rule := policy.Rule{
			Name:              r.GetName(),
			Priority:          int(r.GetPriority()),
			MinSeverity:       severityOf(r.GetMinSeverity()),
			FixVersionOnly:    r.GetFixVersionOnly(),
			AllowedLicenses:   r.GetAllowedLicenses(),
			BannedLicenses:    r.GetBannedLicenses(),
			AllowUnknown:      r.AllowUnknown,
			FailBuild:         r.GetFailBuild(),
			BlockDownload:     r.GetBlockDownload(),
			BlockUnscanned:    r.GetBlockUnscanned(),
			Malicious:         r.GetMalicious(),
			Secrets:           r.GetSecrets(),
			Misconfigurations: r.GetMisconfigurations(),
			Condition:         r.GetCondition(),
			EndOfLife:         r.GetEndOfLife(),
			Unmaintained:      r.GetUnmaintained(),
			IgnoreUnreachable: r.GetIgnoreUnreachable(),
			LiveSecrets:       r.GetLiveSecrets(),
		}
		if r.CvssFrom != nil || r.CvssTo != nil {
			rule.CVSS = &policy.CVSSRange{
				From: r.GetCvssFrom(),
				To:   r.GetCvssTo(),
			}
		}

		out.Rules = append(out.Rules, rule)
	}

	return out
}

func (s *scanServer) ListPolicies(
	ctx context.Context,
	_ *connect.Request[relicv1.ListPoliciesRequest],
) (*connect.Response[relicv1.ListPoliciesResponse], error) {
	list, err := s.d.ScanPolicies.Policies(ctx)
	if err != nil {
		return nil, err
	}

	out := &relicv1.ListPoliciesResponse{}
	for _, p := range list {
		out.Policies = append(out.Policies, policyMessage(p))
	}

	return connect.NewResponse(out), nil
}

func save(create func(bool) error) error {
	err := create(true)
	if errors.Is(err, policy.ErrExists) {
		err = create(false)
	}

	if errors.Is(err, policy.ErrNotFound) {
		err = create(true)
	}

	return err
}

func (s *scanServer) PutPolicy(
	ctx context.Context,
	req *connect.Request[relicv1.PutPolicyRequest],
) (*connect.Response[relicv1.PutPolicyResponse], error) {
	p := policyOf(req.Msg.GetPolicy())

	if err := save(func(create bool) error {
		return s.d.ScanPolicies.SavePolicy(ctx, p, create)
	}); err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.PutPolicyResponse{}), nil
}

func (s *scanServer) DeletePolicy(
	ctx context.Context,
	req *connect.Request[relicv1.DeletePolicyRequest],
) (*connect.Response[relicv1.DeletePolicyResponse], error) {
	if err := s.d.ScanPolicies.DeletePolicy(ctx, req.Msg.GetName()); err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.DeletePolicyResponse{}), nil
}

func (s *scanServer) ListWatches(
	ctx context.Context,
	_ *connect.Request[relicv1.ListWatchesRequest],
) (*connect.Response[relicv1.ListWatchesResponse], error) {
	list, err := s.d.ScanPolicies.Watches(ctx)
	if err != nil {
		return nil, err
	}

	out := &relicv1.ListWatchesResponse{}

	for _, w := range list {
		m := &relicv1.Watch{
			Name:        w.Name,
			Description: w.Description,
			Active:      w.Active,
			Policies:    w.Policies,
		}
		for _, sc := range w.Scopes {
			m.Scopes = append(
				m.Scopes,
				&relicv1.WatchScope{
					Kind:  fromDomain[relicv1.WatchScopeKind](sc.Kind),
					All:   sc.All,
					Names: sc.Names,
				},
			)
		}

		out.Watches = append(out.Watches, m)
	}

	return connect.NewResponse(out), nil
}

func (s *scanServer) PutWatch(
	ctx context.Context,
	req *connect.Request[relicv1.PutWatchRequest],
) (*connect.Response[relicv1.PutWatchResponse], error) {
	m := req.Msg.GetWatch()

	w := policy.Watch{
		Name:        m.GetName(),
		Description: m.GetDescription(),
		Active:      m.GetActive(),
		Scopes:      []policy.Scope{},
		Policies:    m.GetPolicies(),
	}
	for _, sc := range m.GetScopes() {
		kind, ok := toDomain[policy.ScopeKind](sc.GetKind())
		if !ok {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("a watch scope needs a kind"))
		}

		w.Scopes = append(w.Scopes, policy.Scope{
			Kind:  kind,
			All:   sc.GetAll(),
			Names: sc.GetNames(),
		})
	}

	if w.Policies == nil {
		w.Policies = []string{}
	}

	if err := save(func(create bool) error {
		return s.d.ScanPolicies.SaveWatch(ctx, w, create)
	}); err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.PutWatchResponse{}), nil
}

func (s *scanServer) DeleteWatch(
	ctx context.Context,
	req *connect.Request[relicv1.DeleteWatchRequest],
) (*connect.Response[relicv1.DeleteWatchResponse], error) {
	if err := s.d.ScanPolicies.DeleteWatch(ctx, req.Msg.GetName()); err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.DeleteWatchResponse{}), nil
}

func ignoreRuleMessage(r policy.IgnoreRule) *relicv1.IgnoreRule {
	out := &relicv1.IgnoreRule{
		Id:              r.ID,
		Notes:           r.Notes,
		Expires:         ts(r.Expires),
		Vulnerabilities: r.Vulnerabilities,
		Cves:            r.CVEs,
		Licenses:        r.Licenses,
		Watches:         r.Watches,
		Policies:        r.Policies,
		Packages:        r.Packages,
		ArtifactPaths:   r.ArtifactPaths,
	}
	for _, b := range r.Builds {
		out.Builds = append(out.Builds, &relicv1.IgnoredBuild{
			Name:   b.Name,
			Number: b.Number,
		})
	}

	return out
}

func ignoreRuleOf(m *relicv1.IgnoreRule) policy.IgnoreRule {
	out := policy.IgnoreRule{
		Notes:           m.GetNotes(),
		Vulnerabilities: m.GetVulnerabilities(),
		CVEs:            m.GetCves(),
		Licenses:        m.GetLicenses(),
		Watches:         m.GetWatches(),
		Policies:        m.GetPolicies(),
		Packages:        m.GetPackages(),
		ArtifactPaths:   m.GetArtifactPaths(),
	}
	if e := m.GetExpires(); e != nil {
		t := e.AsTime().UTC().Truncate(time.Second)
		out.Expires = &t
	}

	for _, b := range m.GetBuilds() {
		out.Builds = append(out.Builds, policy.Build{
			Name:   b.GetName(),
			Number: b.GetNumber(),
		})
	}

	return out
}

func (s *scanServer) ListIgnoreRules(
	ctx context.Context,
	_ *connect.Request[relicv1.ListIgnoreRulesRequest],
) (*connect.Response[relicv1.ListIgnoreRulesResponse], error) {
	list, err := s.d.ScanPolicies.IgnoreRules(ctx, false)
	if err != nil {
		return nil, err
	}

	out := &relicv1.ListIgnoreRulesResponse{}
	for _, r := range list {
		out.Rules = append(out.Rules, ignoreRuleMessage(r))
	}

	return connect.NewResponse(out), nil
}

func (s *scanServer) CreateIgnoreRule(
	ctx context.Context,
	req *connect.Request[relicv1.CreateIgnoreRuleRequest],
) (*connect.Response[relicv1.CreateIgnoreRuleResponse], error) {
	r, err := s.d.ScanPolicies.CreateIgnoreRule(ctx, ignoreRuleOf(req.Msg.GetRule()), auth.FromContext(ctx).Name())
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.CreateIgnoreRuleResponse{
		Rule: ignoreRuleMessage(r),
	}), nil
}

func (s *scanServer) DeleteIgnoreRule(
	ctx context.Context,
	req *connect.Request[relicv1.DeleteIgnoreRuleRequest],
) (*connect.Response[relicv1.DeleteIgnoreRuleResponse], error) {
	id, err := uuid.Parse(req.Msg.GetId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("ignore rule not found"))
	}

	if err := s.d.ScanPolicies.DeleteIgnoreRule(ctx, id); err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.DeleteIgnoreRuleResponse{}), nil
}
