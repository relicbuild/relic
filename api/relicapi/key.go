package relicapi

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	relicv1 "relic/api/gen/relic/v1"
)

type keyServer struct {
	d Dependencies
}

func (s *keyServer) ListKeyPairs(
	ctx context.Context,
	_ *connect.Request[relicv1.ListKeyPairsRequest],
) (*connect.Response[relicv1.ListKeyPairsResponse], error) {
	pairs, err := s.d.SigningKeys.List(ctx)
	if err != nil {
		return nil, err
	}

	out := &relicv1.ListKeyPairsResponse{
		KeyPairs: make([]*relicv1.KeyPair, 0, len(pairs)),
	}
	for _, k := range pairs {
		out.KeyPairs = append(
			out.KeyPairs,
			&relicv1.KeyPair{
				Name:      k.Name,
				Kind:      k.Kind,
				PublicKey: k.PublicKey,
				Created:   timestamppb.New(k.Created),
			},
		)
	}

	return connect.NewResponse(out), nil
}

func (s *keyServer) GetPublicKey(
	ctx context.Context,
	req *connect.Request[relicv1.GetPublicKeyRequest],
) (*connect.Response[relicv1.GetPublicKeyResponse], error) {
	pub, err := s.d.SigningKeys.PublicKey(ctx, req.Msg.GetName())
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.GetPublicKeyResponse{
		PublicKey: pub,
	}), nil
}
