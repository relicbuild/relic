package relicapi

import (
	"context"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/access/auth"
	"relic/internal/artifacts/repository"
)

type artifactServer struct {
	d Dependencies
}

const defaultSearchLimit = 100

func can(ctx context.Context, d Dependencies, repoKey, path string, action auth.Action) error {
	check, err := repositoryAccess(ctx, d, repoKey)
	if err != nil {
		return err
	}

	return check(path, action)
}

func repositoryAccess(
	ctx context.Context,
	d Dependencies,
	repoKey string,
) (func(path string, action auth.Action) error, error) {
	r, err := d.Repositories.GetRepository(ctx, repoKey)
	if err != nil {
		return nil, err
	}

	p := auth.FromContext(ctx)
	if p.Admin {
		return func(string, auth.Action) error {
			return nil
		}, nil
	}

	perms, err := d.Security.Permissions(ctx)
	if err != nil {
		return nil, err
	}

	target := auth.Repository{
		Name:  r.Name,
		Class: string(r.Class),
	}

	return func(path string, action auth.Action) error {
		if !perms.CanInRepository(p.Subject(), target, path, auth.ActionRead) {
			return repository.ErrNotFound
		}

		if action != auth.ActionRead && !perms.CanInRepository(p.Subject(), target, path, action) {
			return connect.NewError(
				connect.CodePermissionDenied,
				errors.New("no "+string(action)+" permission on "+repoKey+"/"+path),
			)
		}

		return nil
	}, nil
}

func itemMessage(it repository.Item) *relicv1.Item {
	path, name := strings.TrimPrefix(it.Path(), "/"), it.Name
	if name == repository.RootPath {
		path, name = "", ""
	}

	return &relicv1.Item{
		Repository: it.Repository,
		Path:       path,
		Name:       name,
		Folder:     it.IsFolder(),
		Size:       it.Size,
		Checksums: &relicv1.Checksums{
			Sha1:   it.Checksums.SHA1,
			Sha256: it.Checksums.SHA256,
			Md5:    it.Checksums.MD5,
		},
		Created:    timestamppb.New(it.Created),
		CreatedBy:  it.CreatedBy,
		Modified:   timestamppb.New(it.Modified),
		ModifiedBy: it.ModifiedBy,
	}
}

func cleanPath(p string) string { return strings.Trim(p, "/") }

func (s *artifactServer) StatItem(
	ctx context.Context,
	req *connect.Request[relicv1.StatItemRequest],
) (*connect.Response[relicv1.StatItemResponse], error) {
	key, p := req.Msg.GetRepository(), cleanPath(req.Msg.GetPath())
	if err := can(ctx, s.d, key, p, auth.ActionRead); err != nil {
		return nil, err
	}

	it, err := s.d.Repositories.Stat(ctx, key, p)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.StatItemResponse{
		Item: itemMessage(it),
	}), nil
}

func canList(ctx context.Context, d Dependencies, repoKey, p string) error {
	err := can(ctx, d, repoKey, p, auth.ActionRead)
	if !errors.Is(err, repository.ErrNotFound) {
		return err
	}

	r, rerr := d.Repositories.GetRepository(ctx, repoKey)
	if rerr != nil {
		return rerr
	}

	perms, perr := d.Security.Permissions(ctx)
	if perr != nil {
		return perr
	}

	if perms.CanReadRepository(auth.FromContext(ctx).Subject(), auth.Repository{
		Name:  r.Name,
		Class: string(r.Class),
	}) {
		return nil
	}

	return err
}

func (s *artifactServer) ListChildren(
	ctx context.Context,
	req *connect.Request[relicv1.ListChildrenRequest],
) (*connect.Response[relicv1.ListChildrenResponse], error) {
	key, p := req.Msg.GetRepository(), cleanPath(req.Msg.GetPath())
	if err := canList(ctx, s.d, key, p); err != nil {
		return nil, err
	}

	r, err := s.d.Repositories.GetRepository(ctx, key)
	if err != nil {
		return nil, err
	}

	out := &relicv1.ListChildrenResponse{}
	if r.Class == repository.ClassVirtual {
		if out.Items, err = virtualChildren(ctx, s.d, r, p); err != nil {
			return nil, err
		}
	} else if out.Items, err = s.children(ctx, key, p); err != nil {
		return nil, err
	}

	slices.SortStableFunc(out.Items, func(a, b *relicv1.Item) int {
		if a.GetFolder() != b.GetFolder() {
			if a.GetFolder() {
				return -1
			}

			return 1
		}

		return strings.Compare(a.GetName(), b.GetName())
	})

	return connect.NewResponse(out), nil
}

func (s *artifactServer) children(ctx context.Context, key, p string) ([]*relicv1.Item, error) {
	folder, err := s.d.Repositories.Stat(ctx, key, p)
	if err != nil {
		return nil, err
	}

	if !folder.IsFolder() {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New(key+"/"+p+" is not a folder"))
	}

	kids, err := s.d.Repositories.Children(ctx, folder)
	if err != nil {
		return nil, err
	}

	check, err := repositoryAccess(ctx, s.d, key)
	if err != nil {
		return nil, err
	}

	items := make([]*relicv1.Item, 0, len(kids))
	for _, k := range kids {
		if check(k.Path(), auth.ActionRead) == nil {
			items = append(items, itemMessage(k))
		}
	}

	if err := addVulnerabilities(ctx, s.d, key, items); err != nil {
		return nil, err
	}

	return items, nil
}

func (s *artifactServer) SearchItems(
	ctx context.Context,
	req *connect.Request[relicv1.SearchItemsRequest],
) (*connect.Response[relicv1.SearchItemsResponse], error) {
	name := strings.TrimSpace(req.Msg.GetName())
	if utf8.RuneCountInString(name) < 2 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("search needs at least two characters"))
	}

	limit := req.Msg.GetLimit()
	if limit <= 0 || limit > 1000 {
		limit = defaultSearchLimit
	}

	keys := []string{
		req.Msg.GetRepository(),
	}
	if keys[0] == "" {
		all, err := s.d.Repositories.ListRepositories(ctx, "", "")
		if err != nil {
			return nil, err
		}

		keys = keys[:0]

		for _, r := range all {
			if r.Class != repository.ClassVirtual {
				keys = append(keys, r.Name)
			}
		}
	}

	out := &relicv1.SearchItemsResponse{}

	for _, key := range keys {
		hits, err := s.d.Repositories.SearchNames(ctx, key, name, limit)
		if err != nil {
			return nil, err
		}

		check, err := repositoryAccess(ctx, s.d, key)
		if err != nil {
			return nil, err
		}

		var found []*relicv1.Item

		for _, it := range hits {
			if check(it.Path(), auth.ActionRead) == nil {
				found = append(found, itemMessage(it))
			}
		}

		if err := addVulnerabilities(ctx, s.d, key, found); err != nil {
			return nil, err
		}

		out.Items = append(out.Items, found...)
	}

	return connect.NewResponse(out), nil
}

func (s *artifactServer) GetProperties(
	ctx context.Context,
	req *connect.Request[relicv1.GetPropertiesRequest],
) (*connect.Response[relicv1.GetPropertiesResponse], error) {
	key, p := req.Msg.GetRepository(), cleanPath(req.Msg.GetPath())
	if err := can(ctx, s.d, key, p, auth.ActionRead); err != nil {
		return nil, err
	}

	it, err := s.d.Repositories.Stat(ctx, key, p)
	if err != nil {
		return nil, err
	}

	props, err := s.d.Repositories.Properties(ctx, it)
	if err != nil {
		return nil, err
	}

	out := &relicv1.GetPropertiesResponse{
		Properties: make(map[string]*relicv1.PropertyValues, len(props)),
	}
	for k, v := range props {
		out.Properties[k] = &relicv1.PropertyValues{
			Values: v,
		}
	}

	return connect.NewResponse(out), nil
}

func (s *artifactServer) SetProperties(
	ctx context.Context,
	req *connect.Request[relicv1.SetPropertiesRequest],
) (*connect.Response[relicv1.SetPropertiesResponse], error) {
	props := repository.Properties{}

	for k, v := range req.Msg.GetProperties() {
		if err := validProperty(k, v.GetValues()); err != nil {
			return nil, err
		}

		props[k] = v.GetValues()
	}

	it, err := s.annotatable(ctx, req.Msg.GetRepository(), req.Msg.GetPath(), req.Msg.GetRecursive())
	if err != nil {
		return nil, err
	}

	if err := s.d.Repositories.SetProperties(ctx, it, props, req.Msg.GetRecursive()); err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.SetPropertiesResponse{}), nil
}

func (s *artifactServer) DeleteProperties(
	ctx context.Context,
	req *connect.Request[relicv1.DeletePropertiesRequest],
) (*connect.Response[relicv1.DeletePropertiesResponse], error) {
	it, err := s.annotatable(ctx, req.Msg.GetRepository(), req.Msg.GetPath(), req.Msg.GetRecursive())
	if err != nil {
		return nil, err
	}

	if err := s.d.Repositories.DeleteProperties(ctx, it, req.Msg.GetKeys(), req.Msg.GetRecursive()); err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.DeletePropertiesResponse{}), nil
}

const (
	maxPropertyValues = 100
	maxPropertyValue  = 4096
)

func validProperty(k string, values []string) error {
	switch {
	case !repository.ValidPropertyKey(k):
		return connect.NewError(connect.CodeInvalidArgument, errors.New(repository.PropertyKeyRule))
	case len(values) == 0 || len(values) > maxPropertyValues:
		return connect.NewError(connect.CodeInvalidArgument, errors.New("give 1 to 100 values"))
	}

	for _, v := range values {
		if len(v) > maxPropertyValue {
			return connect.NewError(connect.CodeInvalidArgument, errors.New("a value is at most 4096 bytes"))
		}
	}

	return nil
}

func (s *artifactServer) annotatable(ctx context.Context, key, p string, recursive bool) (repository.Item, error) {
	p = cleanPath(p)
	if err := can(ctx, s.d, key, p, auth.ActionAnnotate); err != nil {
		return repository.Item{}, err
	}

	it, err := s.d.Repositories.Stat(ctx, key, p)
	if err != nil {
		return repository.Item{}, err
	}

	if recursive && it.IsFolder() && !auth.FromContext(ctx).Admin {
		below, err := s.d.Repositories.Subtree(ctx, it)
		if err != nil {
			return repository.Item{}, err
		}

		check, err := repositoryAccess(ctx, s.d, key)
		if err != nil {
			return repository.Item{}, err
		}

		for _, sub := range below {
			if err := check(sub.Path(), auth.ActionAnnotate); err != nil {
				return repository.Item{}, connect.NewError(
					connect.CodePermissionDenied,
					errors.New("no annotate permission on "+key+"/"+sub.Path()),
				)
			}
		}
	}

	return it, nil
}

func canWriteDestination(ctx context.Context, d Dependencies, repoKey, path string) error {
	err := can(ctx, d, repoKey, path, auth.ActionWrite)

	switch {
	case errors.Is(err, repository.ErrRepositoryNotFound):
	case errors.Is(err, repository.ErrNotFound):
		readable, err := canReadSomewhere(ctx, d, repoKey)
		if err != nil {
			return err
		}

		if readable {
			return connect.NewError(
				connect.CodePermissionDenied,
				errors.New("no write permission on "+repoKey+"/"+path),
			)
		}
	default:
		return err
	}

	return connect.NewError(
		connect.CodeNotFound,
		errors.New("destination "+repoKey+"/"+path+" not found"),
	)
}

func canReadSomewhere(ctx context.Context, d Dependencies, repoKey string) (bool, error) {
	p := auth.FromContext(ctx)
	if p.Admin || p.Viewer {
		return true, nil
	}

	r, err := d.Repositories.GetRepository(ctx, repoKey)
	if err != nil {
		return false, err
	}

	perms, err := d.Security.Permissions(ctx)
	if err != nil {
		return false, err
	}

	return perms.CanReadRepository(p.Subject(), auth.Repository{
		Name:  r.Name,
		Class: string(r.Class),
	}), nil
}

func (s *artifactServer) copyOrMove(ctx context.Context, key, p, toKey, toPath string, move, dryRun bool) error {
	p, toPath = cleanPath(p), cleanPath(toPath)

	source := auth.ActionRead
	if move {
		source = auth.ActionDelete
	}

	if err := can(ctx, s.d, key, p, source); err != nil {
		return err
	}

	if err := canWriteDestination(ctx, s.d, toKey, toPath); err != nil {
		return err
	}

	it, err := s.d.Repositories.Stat(ctx, key, p)
	if err != nil {
		return err
	}

	in := repository.CopyInput{
		Source:                it,
		DestinationRepository: toKey,
		DestinationPath:       toPath,
		Move:                  move,
		DryRun:                dryRun,
		User:                  auth.FromContext(ctx).Name(),
		CheckUploadLimit:      true,
	}
	if s.d.DownloadBlocker != nil {
		in.RefuseFile = s.d.DownloadBlocker.RefuseCopy
	}

	if in.Permits, err = repositoryPermits(ctx, s.d, key, toKey); err != nil {
		return err
	}

	return s.d.Repositories.Copy(ctx, in)
}

func repositoryPermits(
	ctx context.Context,
	d Dependencies,
	repoKeys ...string,
) (func(repoKey, path string, action auth.Action) bool, error) {
	if auth.FromContext(ctx).Admin {
		return nil, nil
	}

	checks := make(map[string]func(string, auth.Action) error, len(repoKeys))
	for _, key := range repoKeys {
		check, err := repositoryAccess(ctx, d, key)
		if err != nil {
			return nil, err
		}

		checks[key] = check
	}

	return func(repoKey, path string, action auth.Action) bool {
		check, ok := checks[repoKey]

		return ok && check(path, action) == nil
	}, nil
}

func (s *artifactServer) CopyItem(
	ctx context.Context,
	req *connect.Request[relicv1.CopyItemRequest],
) (*connect.Response[relicv1.CopyItemResponse], error) {
	m := req.Msg
	if err := s.copyOrMove(
		ctx,
		m.GetRepository(),
		m.GetPath(),
		m.GetToRepository(),
		m.GetToPath(),
		false,
		m.GetDryRun(),
	); err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.CopyItemResponse{}), nil
}

func (s *artifactServer) MoveItem(
	ctx context.Context,
	req *connect.Request[relicv1.MoveItemRequest],
) (*connect.Response[relicv1.MoveItemResponse], error) {
	m := req.Msg
	if err := s.copyOrMove(
		ctx,
		m.GetRepository(),
		m.GetPath(),
		m.GetToRepository(),
		m.GetToPath(),
		true,
		m.GetDryRun(),
	); err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.MoveItemResponse{}), nil
}

// DeleteItem deletes a file or folder.
func (s *artifactServer) DeleteItem(
	ctx context.Context,
	req *connect.Request[relicv1.DeleteItemRequest],
) (*connect.Response[relicv1.DeleteItemResponse], error) {
	key, p := req.Msg.GetRepository(), cleanPath(req.Msg.GetPath())
	if p == "" {
		return nil, connect.NewError(
			connect.CodeInvalidArgument,
			errors.New("delete the repository with RepositoryService.DeleteRepository"),
		)
	}

	if err := can(ctx, s.d, key, p, auth.ActionDelete); err != nil {
		return nil, err
	}

	r, err := s.d.Repositories.GetRepository(ctx, key)
	if err != nil {
		return nil, err
	}

	if r.Class == repository.ClassVirtual {
		return nil, connect.NewError(
			connect.CodeInvalidArgument,
			errors.New("virtual repositories hold no content; delete it in a member repository"),
		)
	}

	it, err := s.d.Repositories.Stat(ctx, key, p)
	if err != nil {
		return nil, err
	}

	var refuseFile func(context.Context, repository.Item) error

	if it.IsFolder() && !auth.FromContext(ctx).Admin {
		check, err := repositoryAccess(ctx, s.d, key)
		if err != nil {
			return nil, err
		}

		refuseFile = func(_ context.Context, file repository.Item) error {
			if err := check(file.Path(), auth.ActionDelete); err != nil {
				return connect.NewError(
					connect.CodePermissionDenied,
					errors.New("no delete permission on "+key+"/"+file.Path()),
				)
			}

			return nil
		}
	}

	if err := s.d.Repositories.DeleteToTrashChecked(ctx, key, p, auth.FromContext(ctx).Name(), refuseFile); err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.DeleteItemResponse{}), nil
}
