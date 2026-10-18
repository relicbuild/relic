package relicapi

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"uuid"

	"connectrpc.com/connect/v2"
	packageurl "github.com/package-url/packageurl-go"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/access/auth"
	"relic/internal/artifacts/repository"
	"relic/internal/scanning/scan"
	"relic/internal/scanning/scan/policy"
)

const maxScanPackages = 10000

const packageScanExtension = "relic"

type packageScanScope struct {
	Watches    []string `json:"watches,omitempty"`
	Repository string   `json:"repository,omitempty"`
}

func (s *scanServer) ScanPackages(
	ctx context.Context,
	req *relicv1.ScanPackagesRequest,
) (*relicv1.ScanPackagesResponse, error) {
	m := req

	purls := slices.Compact(slices.Sorted(slices.Values(m.GetPurls())))
	if len(purls) == 0 || len(purls) > maxScanPackages {
		return nil, connect.NewError(connect.CodeInvalidArgument, "give 1 to 10000 package URLs")
	}

	for _, p := range purls {
		if _, err := packageurl.FromString(p); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, "invalid package URL: "+p)
		}
	}

	if s.d.Scans == nil || s.d.QueueGraphScan == nil {
		return nil, connect.NewError(connect.CodeUnavailable, "scanning is not available")
	}

	if err := s.existingScope(ctx, m.GetWatches(), m.GetRepository()); err != nil {
		return nil, err
	}

	scope, err := json.Marshal(packageScanScope{
		Watches:    m.GetWatches(),
		Repository: m.GetRepository(),
	})
	if err != nil {
		return nil, err
	}

	id, err := s.d.Scans.QueueGraph(ctx, auth.FromContext(ctx).Name(), scan.GraphRequest{
		PURLs: purls,
		Extensions: map[string]json.RawMessage{
			packageScanExtension: scope,
		},
	}, nil)
	if err != nil {
		return nil, err
	}

	if err := s.d.QueueGraphScan(ctx, id); err != nil {
		return nil, errors.Join(err, s.d.Scans.FailGraph(context.WithoutCancel(ctx), id, err))
	}

	return &relicv1.ScanPackagesResponse{
		Id: id.String(),
	}, nil
}

func (s *scanServer) existingScope(ctx context.Context, watches []string, repo string) error {
	for _, name := range watches {
		_, err := s.d.ScanPolicies.Watch(ctx, name)
		if errors.Is(err, policy.ErrNotFound) {
			return connect.Errorf(connect.CodeInvalidArgument, "watch %q does not exist", name)
		}

		if err != nil {
			return err
		}
	}

	if repo == "" {
		return nil
	}

	missing := connect.Errorf(connect.CodeInvalidArgument, "repository %q does not exist", repo)

	r, err := s.d.Repositories.GetRepository(ctx, repo)
	if errors.Is(err, repository.ErrRepositoryNotFound) {
		return missing
	}

	if err != nil {
		return err
	}

	readable, err := (&repositoryServer{
		d: s.d,
	}).canRead(ctx, r)
	if err != nil {
		return err
	}

	if !readable {
		return missing
	}

	return nil
}

func (s *scanServer) GetPackagesScan(
	ctx context.Context,
	req *relicv1.GetPackagesScanRequest,
) (*relicv1.GetPackagesScanResponse, error) {
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, scan.ErrNoScan
	}

	g, err := s.d.ScanResults.GraphScan(ctx, id)
	if err != nil {
		return nil, err
	}

	if p := auth.FromContext(ctx); !p.Admin && g.Username != p.Name() {
		return nil, scan.ErrNoScan
	}

	var scope packageScanScope
	if raw := g.Request.Extensions[packageScanExtension]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &scope); err != nil {
			return nil, err
		}
	}

	res, err := s.graphResult(ctx, g, func(w policy.Watch) bool {
		return slices.Contains(scope.Watches, w.Name) ||
			(scope.Repository != "" && w.Covers(policy.ScopeRepository, scope.Repository))
	}, policy.Subject{})
	if err != nil {
		return nil, err
	}

	return &relicv1.GetPackagesScanResponse{
		Scan:          res.scan,
		Violations:    res.violations,
		FailBuild:     res.failBuild,
		BlockDownload: res.blockDownload,
	}, nil
}
