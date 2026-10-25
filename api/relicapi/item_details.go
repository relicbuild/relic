package relicapi

import (
	"context"
	"errors"
	"maps"
	"path"
	"regexp"
	"slices"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/timestamppb"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/access/auth"
	"relic/internal/artifacts/formats/docker"
	"relic/internal/artifacts/repository"
	"relic/internal/scanning/scan"
	"relic/internal/scanning/scan/policy"
)

func scanTarget(
	ctx context.Context,
	d Dependencies,
	it repository.Item,
) (target repository.Item, source string, ok bool, err error) {
	if !it.IsFolder() {
		return it, "", true, nil
	}

	m, err := d.Repositories.Stat(ctx, it.Repository, path.Join(cleanPath(it.Path()), imageManifest))
	if errors.Is(err, repository.ErrNotFound) {
		return repository.Item{}, "", false, nil
	}

	if err != nil {
		return repository.Item{}, "", false, err
	}

	return m, imageManifest, !m.IsFolder(), nil
}

func scanStatusMessage(status string) relicv1.ScanStatus {
	n, _ := enumNumber(relicv1.ScanStatus(0).Descriptor(), status)
	return relicv1.ScanStatus(n)
}

var scannable = regexp.MustCompile(`(?i)` + scan.ScannablePattern)

const scanOffReason = "Relic Scan is off: no worker with Syft and Grype reported lately"

func (d Dependencies) scanning(ctx context.Context) (bool, error) {
	if d.ScanWorkers == nil {
		return true, nil
	}

	return d.ScanWorkers(ctx)
}

func waitingScan(ctx context.Context, d Dependencies, reason string) (relicv1.ScanStatus, string, error) {
	on, err := d.scanning(ctx)
	if err != nil {
		return 0, "", err
	}

	if !on {
		return relicv1.ScanStatus_SCAN_STATUS_UNAVAILABLE, scanOffReason, nil
	}

	return relicv1.ScanStatus_SCAN_STATUS_QUEUED, reason, nil
}

func itemScan(ctx context.Context, d Dependencies, it repository.Item) (*relicv1.ItemScan, error) {
	out := &relicv1.ItemScan{
		Status: relicv1.ScanStatus_SCAN_STATUS_NONE,
		Counts: map[string]int32{},
	}

	target, source, ok, err := scanTarget(ctx, d, it)
	if err != nil || !ok {
		return out, err
	}

	a, err := d.ScanResults.Artifact(ctx, target.Checksums.SHA256)
	if errors.Is(err, scan.ErrNoScan) {
		if !scannable.MatchString(target.Name) {
			return out, nil
		}

		if out.Status, out.Reason, err = waitingScan(ctx, d, "not picked up by the scanner yet"); err != nil {
			return nil, err
		}

		return out, nil
	}

	if err != nil {
		return nil, err
	}

	out.Status = scanStatusMessage(a.Status)
	out.Reason = a.Reason

	if a.Status == scan.StatusQueued {
		if out.Status, out.Reason, err = waitingScan(ctx, d, a.Reason); err != nil {
			return nil, err
		}
	}

	out.Components = int32(a.Components) //nolint:gosec // a component count
	out.VulnerabilityDatabaseBuilt = ts(a.DBBuilt)
	out.Source = source

	findings, err := d.ScanResults.Findings(ctx, a.Sha256)
	if err != nil {
		return nil, err
	}

	reachability, err := d.ScanResults.Reachability(ctx, []string{
		a.Sha256,
	})
	if err != nil {
		return nil, err
	}

	for _, f := range findings {
		m := findingMessage(f)
		m.Reachability, m.ReachabilityTrace = reachability.State(a.Sha256, f.ID, f.CVEs(), f.Package.PURL)
		out.Findings = append(out.Findings, m)
		out.Counts[string(f.Severity)]++
	}

	issues, err := d.ScanResults.Issues(ctx, a.Sha256)
	if err != nil {
		return nil, err
	}

	for _, i := range issues {
		out.Issues = append(out.Issues, &relicv1.ScanIssue{
			Analyzer:   i.Analyzer,
			Rule:       i.Rule,
			Severity:   severityMessage(i.Severity),
			Title:      i.Title,
			File:       i.File,
			Line:       int32(min(i.Line, 1<<30)), //nolint:gosec // bounded
			Resource:   i.Resource,
			Validation: i.Validation,
			Validated:  ts(i.Validated),
		})
	}

	risks, err := d.ScanResults.OperationalRisks(ctx, a.Sha256)
	if err != nil {
		return nil, err
	}

	for _, r := range risks {
		out.Risks = append(out.Risks, operationalRiskMessage(r))
	}

	// Enum values run from the most severe down.
	slices.SortStableFunc(
		out.Findings,
		func(a, b *relicv1.Finding) int {
			return int(a.GetSeverity()) - int(b.GetSeverity())
		},
	)

	return out, nil
}

func addVulnerabilities(ctx context.Context, d Dependencies, repoKey string, items []*relicv1.Item) error {
	content := map[string][]int{}
	folders := map[string]int{}

	for i, it := range items {
		if it.GetFolder() {
			folders[path.Join(it.GetPath(), imageManifest)] = i
		} else if sha := it.GetChecksums().GetSha256(); sha != "" {
			content[sha] = append(content[sha], i)
		}
	}

	if len(folders) > 0 {
		parents := make([]string, 0, len(folders))
		for p := range folders {
			parents = append(parents, path.Dir(p))
		}

		manifests, err := d.ScanResults.ImageManifests(ctx, repoKey, parents)
		if err != nil {
			return err
		}

		for folder, sha := range manifests {
			content[sha] = append(content[sha], folders[path.Join(folder, imageManifest)])
		}
	}

	if len(content) == 0 {
		return nil
	}

	counts, err := d.ScanResults.SeverityCounts(ctx, slices.Collect(maps.Keys(content)))
	if err != nil {
		return err
	}

	for sha, bySeverity := range counts {
		for _, i := range content[sha] {
			if items[i].Vulnerabilities == nil {
				items[i].Vulnerabilities = map[string]int32{}
			}

			for sev, n := range bySeverity {
				items[i].Vulnerabilities[string(sev)] += int32(n) //nolint:gosec // a finding count
			}
		}
	}

	return nil
}

func (s *artifactServer) GetItemDetails(
	ctx context.Context,
	req *relicv1.GetItemDetailsRequest,
) (*relicv1.GetItemDetailsResponse, error) {
	key, p := req.GetRepository(), cleanPath(req.GetPath())
	if err := canList(ctx, s.d, key, p); err != nil {
		return nil, err
	}

	r, err := s.d.Repositories.GetRepository(ctx, key)
	if err != nil {
		return nil, err
	}

	if r.Class == repository.ClassVirtual && p != "" {
		if key, err = virtualHolder(ctx, s.d, r, p); err != nil {
			return nil, err
		}

		if r, err = s.d.Repositories.GetRepository(ctx, key); err != nil {
			return nil, err
		}
	}

	it, err := s.d.Repositories.Stat(ctx, key, p)
	if err != nil {
		return nil, err
	}

	props, err := s.d.Repositories.Properties(ctx, it)
	if err != nil {
		return nil, err
	}

	out := &relicv1.GetItemDetailsResponse{
		Item:       itemMessage(it),
		Format:     formatMessage(r.Format),
		Properties: map[string]*relicv1.PropertyValues{},
	}
	for k, v := range props {
		out.Properties[k] = &relicv1.PropertyValues{
			Values: v,
		}
	}

	if out.Scan, err = itemScan(ctx, s.d, it); err != nil {
		return nil, err
	}

	if p != "" && r.Class != repository.ClassVirtual {
		out.CanDelete = can(ctx, s.d, key, p, auth.ActionDelete) == nil
		out.CanAnnotate = can(ctx, s.d, key, p, auth.ActionAnnotate) == nil
	}

	if !it.IsFolder() {
		if err := s.addBuildLinks(ctx, out, it.Checksums.SHA1); err != nil {
			return nil, err
		}

		d, err := s.d.DownloadStats.Of(ctx, it.ID)
		if err != nil {
			return nil, err
		}

		if d.Count > 0 {
			out.Downloads = &relicv1.DownloadStats{
				Count:            d.Count,
				LastDownloadedAt: timestamppb.New(d.LastDownloadedAt),
			}
			if auth.FromContext(ctx).Admin {
				out.Downloads.LastDownloadedBy = d.LastDownloadedBy
			}
		}
	}

	if it.IsFolder() && r.Format == repository.FormatDocker {
		out.Image = out.GetScan().GetSource() != ""
		if !out.Image {
			_, err := s.d.Repositories.Stat(ctx, key, path.Join(p, docker.ListFile))
			if err != nil && !errors.Is(err, repository.ErrNotFound) {
				return nil, err
			}

			out.Image = err == nil
		}
	}

	return out, nil
}

func (s *artifactServer) addBuildLinks(ctx context.Context, out *relicv1.GetItemDetailsResponse, sha1 string) error {
	b := &buildServer{
		s.d,
	}

	produced, err := s.d.Builds.Producing(ctx, sha1)
	if err != nil {
		return err
	}

	for _, l := range produced {
		if ok, err := b.canRead(ctx, l.Project, l.Name); err == nil && ok {
			out.ProducedBy = append(
				out.ProducedBy,
				&relicv1.BuildLink{
					Name:    l.Name,
					Number:  l.Number,
					Started: timestamppb.New(l.Started),
					Module:  l.Module,
					Project: l.Project,
				},
			)
		}
	}

	used, err := s.d.Builds.Using(ctx, sha1)
	if err != nil {
		return err
	}

	for _, l := range used {
		if ok, err := b.canRead(ctx, l.Project, l.Name); err == nil && ok {
			out.UsedBy = append(
				out.UsedBy,
				&relicv1.BuildLink{
					Name:    l.Name,
					Number:  l.Number,
					Started: timestamppb.New(l.Started),
					Module:  l.Module,
					Project: l.Project,
					Scopes:  l.Scopes,
				},
			)
		}
	}

	return nil
}

func (s *artifactServer) GetEffectivePermissions(
	ctx context.Context,
	req *relicv1.GetEffectivePermissionsRequest,
) (*relicv1.GetEffectivePermissionsResponse, error) {
	key, p := req.GetRepository(), cleanPath(req.GetPath())
	if err := canList(ctx, s.d, key, p); err != nil {
		return nil, err
	}

	r, err := s.d.Repositories.GetRepository(ctx, key)
	if err != nil {
		return nil, err
	}

	perms, err := s.d.Security.Permissions(ctx)
	if err != nil {
		return nil, err
	}

	out := &relicv1.GetEffectivePermissionsResponse{}
	for _, g := range perms.Effective(auth.Repository{
		Name:  r.Name,
		Class: string(r.Class),
	}, p) {
		out.Grants = append(out.Grants, &relicv1.EffectiveGrant{
			Principal:   g.Principal,
			Group:       g.Group,
			Permissions: g.Permissions,
			Actions:     actionMessages(g.Actions),
		})
	}

	return out, nil
}

func (s *scanServer) GetItemSbom(
	ctx context.Context,
	req *relicv1.GetItemSbomRequest,
) (*relicv1.GetItemSbomResponse, error) {
	key, p := req.GetRepository(), cleanPath(req.GetPath())
	if err := can(ctx, s.d, key, p, auth.ActionRead); err != nil {
		return nil, err
	}

	it, err := s.d.Repositories.Stat(ctx, key, p)
	if err != nil {
		return nil, err
	}

	target, source, ok, err := scanTarget(ctx, s.d, it)
	if err != nil {
		return nil, err
	}

	var a scan.ArtifactScan
	if ok {
		a, err = s.d.ScanResults.Artifact(ctx, target.Checksums.SHA256)
	}

	if !ok || errors.Is(err, scan.ErrNoScan) || (err == nil && a.SBOM == nil) {
		return nil, connect.NewError(connect.CodeNotFound, "no SBOM for this item")
	}

	if err != nil {
		return nil, err
	}

	comps, err := scan.Components(a.SBOM)
	if err != nil {
		return nil, err
	}

	out := &relicv1.GetItemSbomResponse{
		Source: source,
	}

	for _, c := range comps {
		m := &relicv1.Component{
			Name:       c.Name,
			Version:    c.Version,
			Type:       c.Type,
			Purl:       c.PURL,
			Licenses:   c.Licenses,
			LicenseIds: c.LicenseIDs,
		}
		for _, l := range c.Locations {
			m.Locations = append(m.Locations, &relicv1.ComponentLocation{
				Path:  l.Path,
				Layer: l.LayerID,
			})
		}

		out.Components = append(out.Components, m)
	}

	violations, err := s.licenseViolations(ctx, it, comps)
	if err != nil {
		return nil, err
	}

	for _, v := range violations {
		m := out.Components[v.Component]
		m.Violations = append(m.Violations, &relicv1.LicenseViolation{
			License:       v.License,
			Watch:         v.Watch,
			Policy:        v.Policy,
			Rule:          v.Rule,
			FailBuild:     v.FailBuild,
			BlockDownload: v.BlockDownload,
		})
	}

	return out, nil
}

func (s *scanServer) licenseViolations(
	ctx context.Context,
	it repository.Item,
	comps []scan.Component,
) ([]policy.LicenseViolation, error) {
	watches, err := s.d.ScanPolicies.Watches(ctx)
	if err != nil {
		return nil, err
	}

	watches = slices.DeleteFunc(watches, func(w policy.Watch) bool {
		return !w.Covers(policy.ScopeRepository, it.Repository)
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

	subject := policy.Subject{
		ArtifactPath: it.Repository + "/" + it.Path(),
	}

	return policy.LicenseViolations(watches, pols, ignores, subject, comps, time.Now()), nil
}
