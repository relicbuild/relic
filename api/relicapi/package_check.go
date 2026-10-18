package relicapi

import (
	"context"
	"slices"

	"connectrpc.com/connect/v2"
	packageurl "github.com/package-url/packageurl-go"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/access/auth"
	"relic/internal/artifacts/repository"
	"relic/internal/scanning/endoflife"
	"relic/internal/scanning/scan"
)

func (s *scanServer) CheckPackage(
	ctx context.Context,
	req *relicv1.CheckPackageRequest,
) (*relicv1.CheckPackageResponse, error) {
	purl, err := packageurl.FromString(req.GetPurl())
	if err != nil || purl.Name == "" || purl.Version == "" {
		return nil, connect.NewError(
			connect.CodeInvalidArgument,
			"purl must be a package URL with a version, such as pkg:npm/lodash@4.17.21",
		)
	}

	repo := req.GetRepository()
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

	return out, nil
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

// purlFormats are the repository formats that serve each package URL type.
var purlFormats = map[string][]repository.Format{
	packageurl.TypeNPM: {
		repository.FormatNpm,
	},
	packageurl.TypePyPi: {
		repository.FormatPyPI,
	},
	packageurl.TypeMaven: {
		repository.FormatMaven,
		repository.FormatGradle,
		repository.FormatIvy,
		repository.FormatSbt,
	},
	packageurl.TypeGolang: {
		repository.FormatGoModules,
	},
	packageurl.TypeNuget: {
		repository.FormatNuGet,
	},
	packageurl.TypeGem: {
		repository.FormatRubyGems,
	},
	packageurl.TypeCargo: {
		repository.FormatCargo,
	},
	packageurl.TypeConan: {
		repository.FormatConan,
	},
	packageurl.TypeComposer: {
		repository.FormatComposer,
	},
	packageurl.TypeCocoapods: {
		repository.FormatCocoaPods,
	},
	packageurl.TypeConda: {
		repository.FormatConda,
	},
	packageurl.TypeCran: {
		repository.FormatCran,
	},
	packageurl.TypeSwift: {
		repository.FormatSwift,
	},
	packageurl.TypePub: {
		repository.FormatPub,
	},
	packageurl.TypeHuggingface: {
		repository.FormatHuggingFace,
		repository.FormatMachineLearning,
	},
	packageurl.TypeDebian: {
		repository.FormatDebian,
	},
	packageurl.TypeRPM: {
		repository.FormatRPM,
	},
	packageurl.TypeApk: {
		repository.FormatAlpine,
	},
	packageurl.TypeDocker: {
		repository.FormatDocker,
		repository.FormatOCI,
	},
	packageurl.TypeOCI: {
		repository.FormatDocker,
		repository.FormatOCI,
		repository.FormatHelmOCI,
	},
}

// offered refuses a version the repository's npm or PyPI metadata leaves
// out; other formats filter no versions. A package URL of a type the
// repository's format cannot hold is an invalid argument.
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
		return nil, connect.NewError(connect.CodeNotFound, repository.ErrRepositoryNotFound.Error()).
			WithCause(repository.ErrRepositoryNotFound)
	}

	if formats, known := purlFormats[purl.Type]; known && r.Format != repository.FormatGeneric &&
		!slices.Contains(formats, r.Format) {
		return nil, connect.Errorf(
			connect.CodeInvalidArgument,
			"%s is a %s repository: it serves no pkg:%s packages", repo, r.Format, purl.Type,
		)
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
