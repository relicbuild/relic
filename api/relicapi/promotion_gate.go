package relicapi

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/delivery/release"
)

type promotionGateServer struct {
	d Dependencies
}

func promotionGateMessage(g release.Gate) *relicv1.PromotionGate {
	return &relicv1.PromotionGate{
		Name:        g.Name,
		Environment: g.Environment,
		Condition:   g.Condition,
		Description: g.Description,
		Created:     timestamppb.New(g.Created),
		Updated:     timestamppb.New(g.Updated),
	}
}

func (s *promotionGateServer) ListPromotionGates(
	ctx context.Context,
	_ *connect.Request[relicv1.ListPromotionGatesRequest],
) (*connect.Response[relicv1.ListPromotionGatesResponse], error) {
	list, err := s.d.Releases.Gates(ctx)
	if err != nil {
		return nil, err
	}

	out := &relicv1.ListPromotionGatesResponse{
		PromotionGates: make([]*relicv1.PromotionGate, 0, len(list)),
	}
	for _, g := range list {
		out.PromotionGates = append(out.PromotionGates, promotionGateMessage(g))
	}

	return connect.NewResponse(out), nil
}

func (s *promotionGateServer) GetPromotionGate(
	ctx context.Context,
	req *connect.Request[relicv1.GetPromotionGateRequest],
) (*connect.Response[relicv1.GetPromotionGateResponse], error) {
	g, err := s.d.Releases.Gate(ctx, req.Msg.GetName())
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.GetPromotionGateResponse{
		PromotionGate: promotionGateMessage(g),
	}), nil
}

func (s *promotionGateServer) PutPromotionGate(
	ctx context.Context,
	req *connect.Request[relicv1.PutPromotionGateRequest],
) (*connect.Response[relicv1.PutPromotionGateResponse], error) {
	m := req.Msg.GetPromotionGate()

	g, err := s.d.Releases.PutGate(ctx, release.Gate{
		Name:        m.GetName(),
		Environment: m.GetEnvironment(),
		Condition:   m.GetCondition(),
		Description: m.GetDescription(),
	})
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.PutPromotionGateResponse{
		PromotionGate: promotionGateMessage(g),
	}), nil
}

func (s *promotionGateServer) DeletePromotionGate(
	ctx context.Context,
	req *connect.Request[relicv1.DeletePromotionGateRequest],
) (*connect.Response[relicv1.DeletePromotionGateResponse], error) {
	if err := s.d.Releases.DeleteGate(ctx, req.Msg.GetName()); err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.DeletePromotionGateResponse{}), nil
}
