package relicapi

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	packageurl "github.com/package-url/packageurl-go"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/access/auth"
	"relic/internal/artifacts/repository"
	"relic/internal/scanning/endoflife"
	"relic/internal/scanning/scan"
)

func (s *scanServer) CheckPackage(
	ctx context.Context,
	req *connect.Request[relicv1.CheckPackageRequest],
) (*connect.Response[relicv1.CheckPackageResponse], error) {
	purl, err := packageurl.FromString(req.Msg.GetPurl())
	if err != nil || purl.Name == "" || purl.Version == "" {
		return nil, connect.NewError(
			connect.CodeInvalidArgument,
			errors.New("purl must be a package URL with a version, such as pkg:npm/lodash@4.17.21"),
		)
	}

	repo := req.Msg.GetRepository()
	out := &relicv1.CheckPackageResponse{}

	if s.d.MaliciousPackages != nil {
		matches, err := s.d.MaliciousPackages.Match(ctx, []string{
			purl.ToString(),
		})
		if err != nil {
			return nil, err
		}

		for _, m := range matches {
			out.Refusals = append(out.Refusals, &relicv1.PackageRefusal{
				Kind:   relicv1.PackageRefusalKind_PACKAGE_REFUSAL_KIND_MALICIOUS,
				Reason: m.ID + ": " + m.Summary,
			})
		}
	}

	if repo != "" {
		refusal, err := s.offered(ctx, repo, purl)
		if err != nil {
			return nil, err
		}

		if refusal != nil {
			out.Refusals = append(out.Refusals, refusal)
		}
	}

	risks, err := s.packageRisks(ctx, purl)
	if err != nil {
		return nil, err
	}

	for _, r := range risks {
		out.Risks = append(out.Risks, &relicv1.OperationalRisk{
			Kind:           r.Kind,
			Severity:       severityMessage(scan.Severity(r.Severity)),
			Purl:           r.PURL,
			PackageName:    r.Name,
			PackageVersion: r.Version,
			Product:        r.Product,
			Cycle:          r.Cycle,
			EolFrom:        r.EOLFrom,
			Detail:         r.Detail,
		})
	}

	violations, err := s.d.ScanPolicies.PackageViolations(ctx, purl.ToString(), repo, risks)
	if err != nil {
		return nil, err
	}

	for _, v := range violations {
		out.Refusals = append(out.Refusals, &relicv1.PackageRefusal{
			Kind:   relicv1.PackageRefusalKind_PACKAGE_REFUSAL_KIND_POLICY,
			Reason: v.Issue + " violates policy " + v.Policy + " (watch " + v.Watch + ")",
			Policy: v.Policy,
			Watch:  v.Watch,
			Rule:   v.Rule,
		})
	}

	out.Allowed = len(out.Refusals) == 0

	return connect.NewResponse(out), nil
}

// packageRisks are the operational risks of the version purl names, from
// the stored end-of-life data.
func (s *scanServer) packageRisks(ctx context.Context, purl packageurl.PackageURL) ([]endoflife.Risk, error) {
	if s.d.EndOfLife == nil {
		return nil, nil
	}

	name := purl.Name
	if purl.Namespace != "" {
		name = purl.Namespace + "/" + purl.Name
	}

	return s.d.EndOfLife.Assess(ctx, []endoflife.Package{
		{
			PURL:    purl.ToString(),
			Name:    name,
			Version: purl.Version,
		},
	}, endoflife.Distro{})
}

// offered refuses a version the repository's npm or PyPI metadata leaves
// out; other formats filter no versions.
func (s *scanServer) offered(
	ctx context.Context,
	repo string,
	purl packageurl.PackageURL,
) (*relicv1.PackageRefusal, error) {
	r, err := s.d.Repositories.GetRepository(ctx, repo)
	if err != nil {
		return nil, err
	}

	perms, err := s.d.Security.Permissions(ctx)
	if err != nil {
		return nil, err
	}

	if !perms.CanReadRepository(auth.FromContext(ctx).Subject(), auth.Repository{
		Name:  r.Name,
		Class: string(r.Class),
	}) {
		return nil, connect.NewError(connect.CodeNotFound, repository.ErrRepositoryNotFound)
	}

	var ok bool

	switch {
	case purl.Type == packageurl.TypeNPM && r.Format == repository.FormatNpm && s.d.NpmPackages != nil:
		name := purl.Name
		if purl.Namespace != "" {
			name = purl.Namespace + "/" + purl.Name
		}

		ok, err = s.d.NpmPackages.Offers(ctx, r.Name, name, purl.Version)
	case purl.Type == packageurl.TypePyPi && r.Format == repository.FormatPyPI && s.d.PythonPackages != nil:
		ok, err = s.d.PythonPackages.Offers(ctx, r, purl.Name, purl.Version)
	default:
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	if ok {
		return nil, nil
	}

	return &relicv1.PackageRefusal{
		Kind: relicv1.PackageRefusalKind_PACKAGE_REFUSAL_KIND_NOT_OFFERED,
		Reason: repo + " does not offer " + purl.Version + ": it is cooling down, quarantined, blocked, " +
			"known malicious, or absent",
	}, nil
}
