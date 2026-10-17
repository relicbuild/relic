package relicapi

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	relicv1 "relic/api/gen/relic/v1"
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
	_ *connect.Request[relicv1.ListCleanupPoliciesRequest],
) (*connect.Response[relicv1.ListCleanupPoliciesResponse], error) {
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

	return connect.NewResponse(out), nil
}

func (s *cleanupServer) GetCleanupPolicy(
	ctx context.Context,
	req *connect.Request[relicv1.GetCleanupPolicyRequest],
) (*connect.Response[relicv1.GetCleanupPolicyResponse], error) {
	svc, err := s.service()
	if err != nil {
		return nil, err
	}

	p, err := svc.Get(ctx, req.Msg.GetName())
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.GetCleanupPolicyResponse{
		Policy: cleanupPolicyMessage(p),
	}), nil
}

func (s *cleanupServer) PutCleanupPolicy(
	ctx context.Context,
	req *connect.Request[relicv1.PutCleanupPolicyRequest],
) (*connect.Response[relicv1.PutCleanupPolicyResponse], error) {
	svc, err := s.service()
	if err != nil {
		return nil, err
	}

	if err := svc.Put(ctx, domainCleanupPolicy(req.Msg.GetPolicy())); err != nil {
		return nil, err
	}

	p, err := svc.Get(ctx, req.Msg.GetPolicy().GetName())
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.PutCleanupPolicyResponse{
		Policy: cleanupPolicyMessage(p),
	}), nil
}

func (s *cleanupServer) DeleteCleanupPolicy(
	ctx context.Context,
	req *connect.Request[relicv1.DeleteCleanupPolicyRequest],
) (*connect.Response[relicv1.DeleteCleanupPolicyResponse], error) {
	svc, err := s.service()
	if err != nil {
		return nil, err
	}

	if err := svc.Delete(ctx, req.Msg.GetName()); err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.DeleteCleanupPolicyResponse{}), nil
}

func (s *cleanupServer) PreviewCleanupPolicy(
	ctx context.Context,
	req *connect.Request[relicv1.PreviewCleanupPolicyRequest],
) (*connect.Response[relicv1.PreviewCleanupPolicyResponse], error) {
	svc, err := s.service()
	if err != nil {
		return nil, err
	}

	p := domainCleanupPolicy(req.Msg.GetPolicy())

	switch name := req.Msg.GetName(); {
	case name != "":
		if p, err = svc.Get(ctx, name); err != nil {
			return nil, err
		}
	case req.Msg.GetPolicy() == nil:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("give a policy or the name of one"))
	}

	found, err := svc.Preview(ctx, p)
	if err != nil {
		return nil, err
	}

	out := &relicv1.PreviewCleanupPolicyResponse{
		Candidates: make([]*relicv1.CleanupCandidate, 0, len(found)),
	}
	for _, c := range found {
		out.Candidates = append(out.Candidates, &relicv1.CleanupCandidate{
			Repository:     c.Repository,
			Path:           c.Path,
			Size:           c.Size,
			Created:        tsValue(c.Created),
			LastDownloaded: ts(c.LastDownloaded),
		})
	}

	return connect.NewResponse(out), nil
}

func (s *cleanupServer) RunCleanupPolicy(
	ctx context.Context,
	req *connect.Request[relicv1.RunCleanupPolicyRequest],
) (*connect.Response[relicv1.RunCleanupPolicyResponse], error) {
	svc, err := s.service()
	if err != nil {
		return nil, err
	}

	moved, err := svc.Run(ctx, req.Msg.GetName())
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.RunCleanupPolicyResponse{
		Moved: int64(moved),
	}), nil
}
