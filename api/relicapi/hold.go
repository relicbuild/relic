package relicapi

import (
	"context"

	"connectrpc.com/connect"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/access/auth"
)

type holdServer struct {
	d Dependencies
}

func (s *holdServer) ListHolds(
	ctx context.Context,
	_ *connect.Request[relicv1.ListHoldsRequest],
) (*connect.Response[relicv1.ListHoldsResponse], error) {
	holds, err := s.d.Repositories.Holds(ctx)
	if err != nil {
		return nil, err
	}

	out := &relicv1.ListHoldsResponse{
		Holds: make([]*relicv1.Hold, 0, len(holds)),
	}
	for _, h := range holds {
		out.Holds = append(out.Holds, &relicv1.Hold{
			Repository: h.Repository,
			Path:       h.Path,
			Reason:     h.Reason,
			CreatedBy:  h.CreatedBy,
			Created:    tsValue(h.Created),
		})
	}

	return connect.NewResponse(out), nil
}

func (s *holdServer) PutHold(
	ctx context.Context,
	req *connect.Request[relicv1.PutHoldRequest],
) (*connect.Response[relicv1.PutHoldResponse], error) {
	m := req.Msg
	if err := s.d.Repositories.PutHold(
		ctx,
		m.GetRepository(),
		m.GetPath(),
		m.GetReason(),
		auth.FromContext(ctx).Name(),
	); err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.PutHoldResponse{}), nil
}

func (s *holdServer) DeleteHold(
	ctx context.Context,
	req *connect.Request[relicv1.DeleteHoldRequest],
) (*connect.Response[relicv1.DeleteHoldResponse], error) {
	if err := s.d.Repositories.ReleaseHold(ctx, req.Msg.GetRepository(), req.Msg.GetPath()); err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.DeleteHoldResponse{}), nil
}
