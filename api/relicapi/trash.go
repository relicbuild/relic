package relicapi

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/access/auth"
	"relic/internal/artifacts/repository"
)

type trashServer struct {
	d Dependencies
}

func (s *trashServer) ListTrash(
	ctx context.Context,
	req *connect.Request[relicv1.ListTrashRequest],
) (*connect.Response[relicv1.ListTrashResponse], error) {
	m := req.Msg
	if kind := m.GetKind(); kind != "" && kind != repository.TypeFile && kind != repository.TypeFolder {
		return nil, connect.NewError(
			connect.CodeInvalidArgument,
			fmt.Errorf("kind %q: file or folder", kind),
		)
	}

	n, size := page(m.GetPage(), m.GetPageSize())

	entries, total, err := s.d.Repositories.TrashEntries(
		ctx,
		repository.TrashQuery{
			Query:              m.GetQuery(),
			OriginalRepository: m.GetOriginalRepository(),
			DeletedBy:          m.GetDeletedBy(),
			Kind:               m.GetKind(),
			Skip:               (n - 1) * size,
			Max:                size,
		},
	)
	if err != nil {
		return nil, err
	}

	repositories, err := s.d.Repositories.TrashOriginalRepositories(ctx)
	if err != nil {
		return nil, err
	}

	deleters, err := s.d.Repositories.TrashDeleters(ctx)
	if err != nil {
		return nil, err
	}

	out := &relicv1.ListTrashResponse{
		Entries:              make([]*relicv1.TrashEntry, 0, len(entries)),
		Total:                total,
		OriginalRepositories: repositories,
		DeletedByUsers:       deleters,
	}
	for _, e := range entries {
		out.Entries = append(out.Entries, &relicv1.TrashEntry{
			Path:               e.Item.Path(),
			Name:               e.Item.Name,
			Folder:             e.Item.IsFolder(),
			Size:               e.Item.Size,
			OriginalRepository: e.OriginalRepository,
			OriginalPath:       e.OriginalPath,
			DeletedBy:          e.DeletedBy,
			Deleted:            timestamppb.New(e.Deleted),
		})
	}

	return connect.NewResponse(out), nil
}

func (s *trashServer) RestoreTrash(
	ctx context.Context,
	req *connect.Request[relicv1.RestoreTrashRequest],
) (*connect.Response[relicv1.RestoreTrashResponse], error) {
	m := req.Msg
	if cleanPath(m.GetPath()) == "" || (m.GetToRepository() == "") != (cleanPath(m.GetToPath()) == "") {
		return nil, connect.NewError(
			connect.CodeInvalidArgument,
			errors.New("give the item's path, and a destination as repository and path"),
		)
	}

	err := s.d.Repositories.RestoreFromTrash(
		ctx,
		cleanPath(m.GetPath()),
		m.GetToRepository(),
		cleanPath(m.GetToPath()),
		auth.FromContext(ctx).Name(),
	)
	if errors.Is(err, repository.ErrNoRestoreTarget) {
		return nil, connect.NewError(
			connect.CodeFailedPrecondition,
			errors.New("the original location is unknown; choose a destination"),
		)
	}

	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.RestoreTrashResponse{}), nil
}

func (s *trashServer) DeleteTrashEntry(
	ctx context.Context,
	req *connect.Request[relicv1.DeleteTrashEntryRequest],
) (*connect.Response[relicv1.DeleteTrashEntryResponse], error) {
	p := cleanPath(req.Msg.GetPath())
	if p == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("use EmptyTrash to clear the trash can"))
	}

	if err := s.d.Repositories.Delete(ctx, s.d.Repositories.TrashRepository(), p); err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.DeleteTrashEntryResponse{}), nil
}

func (s *trashServer) EmptyTrash(
	ctx context.Context,
	_ *connect.Request[relicv1.EmptyTrashRequest],
) (*connect.Response[relicv1.EmptyTrashResponse], error) {
	if err := s.d.Repositories.EmptyTrash(ctx); err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.EmptyTrashResponse{}), nil
}
