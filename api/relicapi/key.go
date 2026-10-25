package relicapi

import (
	"context"
	"fmt"
	"slices"

	"google.golang.org/protobuf/types/known/timestamppb"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/artifacts/signingkey"
	"relic/internal/delivery/release"
)

type keyServer struct {
	d Dependencies
}

func (s *keyServer) ListKeyPairs(
	ctx context.Context,
	_ *relicv1.ListKeyPairsRequest,
) (*relicv1.ListKeyPairsResponse, error) {
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

	return out, nil
}

func (s *keyServer) GetPublicKey(
	ctx context.Context,
	req *relicv1.GetPublicKeyRequest,
) (*relicv1.GetPublicKeyResponse, error) {
	pub, err := s.d.SigningKeys.PublicKey(ctx, req.GetName())
	if err != nil {
		return nil, err
	}

	return &relicv1.GetPublicKeyResponse{
		PublicKey: pub,
	}, nil
}

func (s *keyServer) DeleteKeyPair(
	ctx context.Context,
	req *relicv1.DeleteKeyPairRequest,
) (*relicv1.DeleteKeyPairResponse, error) {
	name := req.GetName()
	if name == release.SigningKey {
		return nil, fmt.Errorf("%w: %s signs release manifests", signingkey.ErrInUse, name)
	}

	repos, err := s.d.Repositories.ListRepositories(ctx, "", "")
	if err != nil {
		return nil, err
	}

	for _, r := range repos {
		if slices.Contains(r.KeyPairRefs(), name) {
			return nil, fmt.Errorf("%w: %s signs repository %s", signingkey.ErrInUse, name, r.Name)
		}
	}

	if err := s.d.SigningKeys.Delete(ctx, name); err != nil {
		return nil, err
	}

	return &relicv1.DeleteKeyPairResponse{}, nil
}
