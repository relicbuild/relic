package relicapi

import (
	"context"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/access/auth"
)

type holdServer struct {
	d Dependencies
}

func (s *holdServer) ListHolds(
	ctx context.Context,
	_ *relicv1.ListHoldsRequest,
) (*relicv1.ListHoldsResponse, error) {
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

	return out, nil
}

func (s *holdServer) PutHold(
	ctx context.Context,
	req *relicv1.PutHoldRequest,
) (*relicv1.PutHoldResponse, error) {
	m := req
	if err := s.d.Repositories.PutHold(
		ctx,
		m.GetRepository(),
		m.GetPath(),
		m.GetReason(),
		auth.FromContext(ctx).Name(),
	); err != nil {
		return nil, err
	}

	return &relicv1.PutHoldResponse{}, nil
}

func (s *holdServer) DeleteHold(
	ctx context.Context,
	req *relicv1.DeleteHoldRequest,
) (*relicv1.DeleteHoldResponse, error) {
	if err := s.d.Repositories.ReleaseHold(ctx, req.GetRepository(), req.GetPath()); err != nil {
		return nil, err
	}

	return &relicv1.DeleteHoldResponse{}, nil
}
