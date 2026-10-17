package relicapi

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/access/auth"
	"relic/internal/artifacts/repository"
	"relic/internal/delivery/evidence"
)

type evidenceServer struct {
	d Dependencies
}

func evidenceMessage(e evidence.Evidence) *relicv1.Evidence {
	out := &relicv1.Evidence{
		Id:                e.ID.String(),
		Repository:        e.SubjectRepository,
		Path:              repository.JoinPath(e.SubjectPath, e.SubjectName),
		SubjectSha256:     e.SubjectSHA256,
		PredicateType:     e.PredicateType,
		PredicateSlug:     e.PredicateSlug,
		PredicateCategory: e.PredicateCategory,
		Predicate:         string(e.Predicate),
		Provider:          e.ProviderID,
		KeyAlias:          e.KeyAlias,
		Verified:          e.Verified,
		StoredPath:        e.ItemPath,
		CreatedBy:         e.CreatedBy,
		Created:           timestamppb.New(e.Created),
	}
	for _, a := range e.Attachments {
		out.Attachments = append(
			out.Attachments,
			&relicv1.EvidenceAttachment{
				Name:       a.Name,
				Sha256:     a.SHA256,
				Type:       a.Type,
				Repository: a.Repository,
				Path:       a.Path,
			},
		)
	}

	return out
}

func trustedKeyMessage(k evidence.TrustedKey) *relicv1.TrustedKey {
	return &relicv1.TrustedKey{
		Kid:         k.KID,
		Alias:       k.Alias,
		Type:        k.Type,
		PublicKey:   k.PublicKey,
		Fingerprint: k.Fingerprint,
		IssuedBy:    k.IssuedBy,
		Issued:      timestamppb.New(k.Issued),
	}
}

func (s *evidenceServer) ListEvidence(
	ctx context.Context,
	req *connect.Request[relicv1.ListEvidenceRequest],
) (*connect.Response[relicv1.ListEvidenceResponse], error) {
	key, p := req.Msg.GetRepository(), cleanPath(req.Msg.GetPath())
	if err := can(ctx, s.d, key, p, auth.ActionRead); err != nil {
		return nil, err
	}

	dir, name := repository.SplitPath(p)

	list, err := s.d.Evidence.ForSubject(ctx, key, dir, name)
	if err != nil {
		return nil, err
	}

	out := &relicv1.ListEvidenceResponse{
		Evidence: make([]*relicv1.Evidence, 0, len(list)),
	}
	for _, e := range list {
		out.Evidence = append(out.Evidence, evidenceMessage(e))
	}

	return connect.NewResponse(out), nil
}

func (s *evidenceServer) CreateEvidence(
	ctx context.Context,
	req *connect.Request[relicv1.CreateEvidenceRequest],
) (*connect.Response[relicv1.CreateEvidenceResponse], error) {
	m := req.Msg

	key, p := m.GetRepository(), cleanPath(m.GetPath())
	if p == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("path names the subject file"))
	}

	if err := can(ctx, s.d, key, p, auth.ActionAnnotate); err != nil {
		return nil, err
	}

	places := make([]evidence.AttachmentPlace, 0, len(m.GetAttachments()))
	for _, a := range m.GetAttachments() {
		places = append(
			places,
			evidence.AttachmentPlace{
				Repository: a.GetRepository(),
				Path:       a.GetPath(),
				SHA256:     a.GetSha256(),
			},
		)
	}

	e, err := s.d.Evidence.Create(ctx, evidence.Input{
		Repository:  key,
		Path:        p,
		Envelope:    []byte(m.GetEnvelope()),
		Attachments: places,
		Provider:    m.GetProvider(),
		User:        auth.FromContext(ctx).Name(),
	})
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.CreateEvidenceResponse{
		Evidence: evidenceMessage(e),
	}), nil
}

func (s *evidenceServer) ListTrustedKeys(
	ctx context.Context,
	_ *connect.Request[relicv1.ListTrustedKeysRequest],
) (*connect.Response[relicv1.ListTrustedKeysResponse], error) {
	keys, err := s.d.Evidence.TrustedKeys(ctx)
	if err != nil {
		return nil, err
	}

	out := &relicv1.ListTrustedKeysResponse{
		Keys: make([]*relicv1.TrustedKey, 0, len(keys)),
	}
	for _, k := range keys {
		out.Keys = append(out.Keys, trustedKeyMessage(k))
	}

	return connect.NewResponse(out), nil
}

func (s *evidenceServer) AddTrustedKey(
	ctx context.Context,
	req *connect.Request[relicv1.AddTrustedKeyRequest],
) (*connect.Response[relicv1.AddTrustedKeyResponse], error) {
	k, err := s.d.Evidence.AddTrustedKey(ctx, req.Msg.GetAlias(), req.Msg.GetPublicKey(), auth.FromContext(ctx).Name())
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.AddTrustedKeyResponse{
		Key: trustedKeyMessage(k),
	}), nil
}

func (s *evidenceServer) DeleteTrustedKey(
	ctx context.Context,
	req *connect.Request[relicv1.DeleteTrustedKeyRequest],
) (*connect.Response[relicv1.DeleteTrustedKeyResponse], error) {
	if err := s.d.Evidence.DeleteTrustedKey(ctx, req.Msg.GetKid()); err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.DeleteTrustedKeyResponse{}), nil
}
