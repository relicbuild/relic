package relicapi

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect/v2"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/access/workload"
)

type workloadIdentityServer struct {
	d Dependencies
}

func workloadIdentityMessage(id workload.Identity) *relicv1.WorkloadIdentity {
	m := &relicv1.WorkloadIdentity{
		Name:                 id.Name,
		Enabled:              id.Enabled,
		Issuer:               id.Issuer,
		Audience:             id.Audience,
		Jwks:                 id.JWKS,
		TokenLifetimeMinutes: int32(id.TokenLifetime / time.Minute), //nolint:gosec // at most an hour
		Rules:                make([]*relicv1.WorkloadRule, 0, len(id.Rules)),
	}
	for _, r := range id.Rules {
		m.Rules = append(m.Rules, &relicv1.WorkloadRule{
			Claims: r.Claims,
			User:   r.User,
			Groups: r.Groups,
		})
	}

	return m
}

func domainWorkloadIdentity(m *relicv1.WorkloadIdentity) workload.Identity {
	id := workload.Identity{
		Name:          m.GetName(),
		Enabled:       m.GetEnabled(),
		Issuer:        m.GetIssuer(),
		Audience:      m.GetAudience(),
		JWKS:          m.GetJwks(),
		TokenLifetime: time.Duration(m.GetTokenLifetimeMinutes()) * time.Minute,
		Rules:         make([]workload.Rule, 0, len(m.GetRules())),
	}
	for _, r := range m.GetRules() {
		id.Rules = append(id.Rules, workload.Rule{
			Claims: r.GetClaims(),
			User:   r.GetUser(),
			Groups: r.GetGroups(),
		})
	}

	return id
}

func (s *workloadIdentityServer) ListWorkloadIdentities(
	ctx context.Context,
	_ *relicv1.ListWorkloadIdentitiesRequest,
) (*relicv1.ListWorkloadIdentitiesResponse, error) {
	if s.d.Workloads == nil {
		return nil, unimplemented("workload identities")
	}

	ids, err := s.d.Workloads.Identities(ctx)
	if err != nil {
		return nil, err
	}

	out := &relicv1.ListWorkloadIdentitiesResponse{
		WorkloadIdentities: make([]*relicv1.WorkloadIdentity, 0, len(ids)),
	}
	for _, id := range ids {
		out.WorkloadIdentities = append(out.WorkloadIdentities, workloadIdentityMessage(id))
	}

	return out, nil
}

func (s *workloadIdentityServer) GetWorkloadIdentity(
	ctx context.Context,
	req *relicv1.GetWorkloadIdentityRequest,
) (*relicv1.GetWorkloadIdentityResponse, error) {
	if s.d.Workloads == nil {
		return nil, unimplemented("workload identities")
	}

	id, err := s.d.Workloads.Identity(ctx, req.GetName())
	if err != nil {
		return nil, err
	}

	return &relicv1.GetWorkloadIdentityResponse{
		WorkloadIdentity: workloadIdentityMessage(id),
	}, nil
}

func (s *workloadIdentityServer) PutWorkloadIdentity(
	ctx context.Context,
	req *relicv1.PutWorkloadIdentityRequest,
) (*relicv1.PutWorkloadIdentityResponse, error) {
	if s.d.Workloads == nil {
		return nil, unimplemented("workload identities")
	}

	id := domainWorkloadIdentity(req.GetWorkloadIdentity())
	if err := s.d.Workloads.PutIdentity(ctx, id); err != nil {
		return nil, err
	}

	return &relicv1.PutWorkloadIdentityResponse{
		WorkloadIdentity: workloadIdentityMessage(id),
	}, nil
}

func (s *workloadIdentityServer) DeleteWorkloadIdentity(
	ctx context.Context,
	req *relicv1.DeleteWorkloadIdentityRequest,
) (*relicv1.DeleteWorkloadIdentityResponse, error) {
	if s.d.Workloads == nil {
		return nil, unimplemented("workload identities")
	}

	if err := s.d.Workloads.DeleteIdentity(ctx, req.GetName()); err != nil {
		return nil, err
	}

	return &relicv1.DeleteWorkloadIdentityResponse{}, nil
}

func (s *sessionServer) ExchangeWorkloadToken(
	ctx context.Context,
	req *relicv1.ExchangeWorkloadTokenRequest,
) (*relicv1.ExchangeWorkloadTokenResponse, error) {
	if s.d.Workloads == nil {
		return nil, unimplemented("workload identities")
	}

	got, err := s.d.Workloads.Exchange(ctx, req.GetIdentity(), req.GetToken())
	if errors.Is(err, workload.ErrRefused) {
		s.d.Log.InfoContext(ctx, "workload token exchange refused", "identity", req.GetIdentity(), "error", err)

		return nil, connect.NewError(connect.CodeUnauthenticated, "the token exchange was refused")
	}

	if err != nil {
		return nil, err
	}

	return &relicv1.ExchangeWorkloadTokenResponse{
		AccessToken:      got.Token.AccessToken,
		ExpiresInSeconds: int64(got.Token.ExpiresIn / time.Second),
		Username:         got.User,
	}, nil
}
