package relicapi

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"

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
	_ *connect.Request[relicv1.ListWorkloadIdentitiesRequest],
) (*connect.Response[relicv1.ListWorkloadIdentitiesResponse], error) {
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

	return connect.NewResponse(out), nil
}

func (s *workloadIdentityServer) GetWorkloadIdentity(
	ctx context.Context,
	req *connect.Request[relicv1.GetWorkloadIdentityRequest],
) (*connect.Response[relicv1.GetWorkloadIdentityResponse], error) {
	if s.d.Workloads == nil {
		return nil, unimplemented("workload identities")
	}

	id, err := s.d.Workloads.Identity(ctx, req.Msg.GetName())
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.GetWorkloadIdentityResponse{
		WorkloadIdentity: workloadIdentityMessage(id),
	}), nil
}

func (s *workloadIdentityServer) PutWorkloadIdentity(
	ctx context.Context,
	req *connect.Request[relicv1.PutWorkloadIdentityRequest],
) (*connect.Response[relicv1.PutWorkloadIdentityResponse], error) {
	if s.d.Workloads == nil {
		return nil, unimplemented("workload identities")
	}

	id := domainWorkloadIdentity(req.Msg.GetWorkloadIdentity())
	if err := s.d.Workloads.PutIdentity(ctx, id); err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.PutWorkloadIdentityResponse{
		WorkloadIdentity: workloadIdentityMessage(id),
	}), nil
}

func (s *workloadIdentityServer) DeleteWorkloadIdentity(
	ctx context.Context,
	req *connect.Request[relicv1.DeleteWorkloadIdentityRequest],
) (*connect.Response[relicv1.DeleteWorkloadIdentityResponse], error) {
	if s.d.Workloads == nil {
		return nil, unimplemented("workload identities")
	}

	if err := s.d.Workloads.DeleteIdentity(ctx, req.Msg.GetName()); err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.DeleteWorkloadIdentityResponse{}), nil
}

func (s *sessionServer) ExchangeWorkloadToken(
	ctx context.Context,
	req *connect.Request[relicv1.ExchangeWorkloadTokenRequest],
) (*connect.Response[relicv1.ExchangeWorkloadTokenResponse], error) {
	if s.d.Workloads == nil {
		return nil, unimplemented("workload identities")
	}

	got, err := s.d.Workloads.Exchange(ctx, req.Msg.GetIdentity(), req.Msg.GetToken())
	if errors.Is(err, workload.ErrRefused) {
		s.d.Log.InfoContext(ctx, "workload token exchange refused", "identity", req.Msg.GetIdentity(), "error", err)

		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("the token exchange was refused"))
	}

	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.ExchangeWorkloadTokenResponse{
		AccessToken:      got.Token.AccessToken,
		ExpiresInSeconds: int64(got.Token.ExpiresIn / time.Second),
		Username:         got.User,
	}), nil
}
