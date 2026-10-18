package relicapi

import (
	"context"

	"connectrpc.com/connect/v2"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/access/auth"
	"relic/internal/artifacts/cleanup"
)

type cleanupServer struct {
	d Dependencies
}

func cleanupPolicyMessage(p cleanup.Policy) *relicv1.CleanupPolicy {
	//nolint:gosec // days and counts are validated as small
	return &relicv1.CleanupPolicy{
		Name:              p.Name,
		Enabled:           p.Enabled,
		Repositories:      p.Repositories,
		PathPattern:       p.PathPattern,
		OlderThanDays:     int32(p.OlderThanDays),
		NotDownloadedDays: int32(p.NotDownloadedDays),
		KeepLatest:        int32(p.KeepLatest),
		Created:           tsValue(p.Created),
		Updated:           tsValue(p.Updated),
		LastRun:           ts(p.LastRun),
		LastMoved:         p.LastMoved,
		LastError:         p.LastError,
	}
}

func domainCleanupPolicy(m *relicv1.CleanupPolicy) cleanup.Policy {
	return cleanup.Policy{
		Name:              m.GetName(),
		Enabled:           m.GetEnabled(),
		Repositories:      m.GetRepositories(),
		PathPattern:       m.GetPathPattern(),
		OlderThanDays:     int(m.GetOlderThanDays()),
		NotDownloadedDays: int(m.GetNotDownloadedDays()),
		KeepLatest:        int(m.GetKeepLatest()),
	}
}

func (s *cleanupServer) service() (*cleanup.Service, error) {
	if s.d.Cleanup == nil {
		return nil, unimplemented("cleanup policies")
	}

	return s.d.Cleanup, nil
}

func (s *cleanupServer) ListCleanupPolicies(
	ctx context.Context,
	_ *relicv1.ListCleanupPoliciesRequest,
) (*relicv1.ListCleanupPoliciesResponse, error) {
	svc, err := s.service()
	if err != nil {
		return nil, err
	}

	list, err := svc.List(ctx)
	if err != nil {
		return nil, err
	}

	out := &relicv1.ListCleanupPoliciesResponse{
		Policies: make([]*relicv1.CleanupPolicy, 0, len(list)),
	}
	for _, p := range list {
		out.Policies = append(out.Policies, cleanupPolicyMessage(p))
	}

	return out, nil
}

func (s *cleanupServer) GetCleanupPolicy(
	ctx context.Context,
	req *relicv1.GetCleanupPolicyRequest,
) (*relicv1.GetCleanupPolicyResponse, error) {
	svc, err := s.service()
	if err != nil {
		return nil, err
	}

	p, err := svc.Get(ctx, req.GetName())
	if err != nil {
		return nil, err
	}

	return &relicv1.GetCleanupPolicyResponse{
		Policy: cleanupPolicyMessage(p),
	}, nil
}

func (s *cleanupServer) PutCleanupPolicy(
	ctx context.Context,
	req *relicv1.PutCleanupPolicyRequest,
) (*relicv1.PutCleanupPolicyResponse, error) {
	svc, err := s.service()
	if err != nil {
		return nil, err
	}

	if err := svc.Put(ctx, domainCleanupPolicy(req.GetPolicy())); err != nil {
		return nil, err
	}

	p, err := svc.Get(ctx, req.GetPolicy().GetName())
	if err != nil {
		return nil, err
	}

	return &relicv1.PutCleanupPolicyResponse{
		Policy: cleanupPolicyMessage(p),
	}, nil
}

func (s *cleanupServer) DeleteCleanupPolicy(
	ctx context.Context,
	req *relicv1.DeleteCleanupPolicyRequest,
) (*relicv1.DeleteCleanupPolicyResponse, error) {
	svc, err := s.service()
	if err != nil {
		return nil, err
	}

	if err := svc.Delete(ctx, req.GetName()); err != nil {
		return nil, err
	}

	return &relicv1.DeleteCleanupPolicyResponse{}, nil
}

func (s *cleanupServer) PreviewCleanupPolicy(
	ctx context.Context,
	req *relicv1.PreviewCleanupPolicyRequest,
) (*relicv1.PreviewCleanupPolicyResponse, error) {
	svc, err := s.service()
	if err != nil {
		return nil, err
	}

	var found []cleanup.Candidate

	switch name := req.GetName(); {
	case name != "":
		found, err = svc.PreviewByName(ctx, name)
	case req.GetPolicy() != nil:
		found, err = svc.Preview(ctx, domainCleanupPolicy(req.GetPolicy()))
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, "give a policy or the name of one")
	}

	if err != nil {
		return nil, err
	}

	readable, err := s.readableCandidates(ctx, found)
	if err != nil {
		return nil, err
	}

	out := &relicv1.PreviewCleanupPolicyResponse{
		Candidates: make([]*relicv1.CleanupCandidate, 0, len(readable)),
	}
	for _, c := range readable {
		out.Candidates = append(out.Candidates, &relicv1.CleanupCandidate{
			Repository:     c.Repository,
			Path:           c.Path,
			Size:           c.Size,
			Created:        tsValue(c.Created),
			LastDownloaded: ts(c.LastDownloaded),
		})
	}

	return out, nil
}

func (s *cleanupServer) readableCandidates(
	ctx context.Context,
	found []cleanup.Candidate,
) ([]cleanup.Candidate, error) {
	checks := map[string]func(string, auth.Action) error{}
	readable := make([]cleanup.Candidate, 0, len(found))

	for _, c := range found {
		check, ok := checks[c.Repository]
		if !ok {
			var err error
			if check, err = repositoryAccess(ctx, s.d, c.Repository); err != nil {
				return nil, err
			}

			checks[c.Repository] = check
		}

		if check(c.Path, auth.ActionRead) == nil {
			readable = append(readable, c)
		}
	}

	return readable, nil
}

func (s *cleanupServer) RunCleanupPolicy(
	ctx context.Context,
	req *relicv1.RunCleanupPolicyRequest,
) (*relicv1.RunCleanupPolicyResponse, error) {
	svc, err := s.service()
	if err != nil {
		return nil, err
	}

	moved, err := svc.Run(ctx, req.GetName())
	if err != nil {
		return nil, err
	}

	return &relicv1.RunCleanupPolicyResponse{
		Moved: int64(moved),
	}, nil
}
