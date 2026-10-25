package relicapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/timestamppb"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/access/auth"
	"relic/internal/artifacts/repository"
)

type repositoryServer struct {
	d Dependencies
}

var classes = map[relicv1.RepositoryClass]repository.Class{
	relicv1.RepositoryClass_REPOSITORY_CLASS_LOCAL:   repository.ClassLocal,
	relicv1.RepositoryClass_REPOSITORY_CLASS_REMOTE:  repository.ClassRemote,
	relicv1.RepositoryClass_REPOSITORY_CLASS_VIRTUAL: repository.ClassVirtual,
}

func classOf(c repository.Class) relicv1.RepositoryClass {
	for k, v := range classes {
		if v == c {
			return k
		}
	}

	return relicv1.RepositoryClass_REPOSITORY_CLASS_UNSPECIFIED
}

var formats, formatMessages = func() (map[relicv1.Format]repository.Format, map[repository.Format]relicv1.Format) {
	to := map[relicv1.Format]repository.Format{}
	from := map[repository.Format]relicv1.Format{}

	for n, name := range relicv1.Format_name {
		if n == 0 {
			continue
		}

		f := repository.Format(strings.ReplaceAll(strings.ToLower(strings.TrimPrefix(name, "FORMAT_")), "_", ""))
		to[relicv1.Format(n)], from[f] = f, relicv1.Format(n)
	}

	return to, from
}()

func formatOf(f relicv1.Format) (repository.Format, error) {
	if f == relicv1.Format_FORMAT_UNSPECIFIED {
		return "", nil
	}

	rf, ok := formats[f]
	if !ok {
		return "", fmt.Errorf("%w: unknown format %d", repository.ErrInvalidRepository, f)
	}

	return rf, nil
}

func formatMessage(f repository.Format) relicv1.Format { return formatMessages[f] }

// RepositoryMessage renders r; the remote password never leaves the server.
func RepositoryMessage(r repository.Repository) *relicv1.Repository {
	c := r.Config

	out := &relicv1.Repository{
		Name:                r.Name,
		Class:               classOf(r.Class),
		Format:              formatMessage(r.Format),
		Project:             r.Project,
		Description:         c.Description,
		Notes:               c.Notes,
		Environments:        c.Environments,
		SigningKey:          c.SigningKey,
		SecondarySigningKey: c.SecondarySigningKey,
		Architectures:       c.Architectures,
		MetadataDepth:       c.MetadataDepth,
		Created:             timestamppb.New(r.Created),
		Updated:             timestamppb.New(r.Updated),
		MaxUploadSize:       c.MaxUploadSize,
		MaxStorage:          c.MaxStorage,
		Immutable:           c.Immutable,
	}
	if rc := c.Remote; rc != nil {
		out.Remote = &relicv1.RemoteRepositorySettings{
			Url:                 rc.URL,
			Username:            rc.Username,
			Offline:             rc.Offline,
			DisableCache:        rc.DisableCache,
			CachePeriodSeconds:  rc.CachePeriodSeconds,
			TimeoutMilliseconds: rc.TimeoutMilliseconds,
			IndexUrl:            rc.IndexURL,

			MissedRetrievalCachePeriodSeconds: rc.MissedRetrievalCachePeriodSeconds,
			CooldownDays:                      rc.CooldownDays,
			Quarantine:                        rc.Quarantine,
		}
	}

	if vc := c.Virtual; vc != nil {
		out.Virtual = &relicv1.VirtualRepositorySettings{
			Members:           vc.Members,
			DeployTo:          vc.DeployTo,
			LocalOnlyPatterns: vc.LocalOnlyPatterns,
		}
	}

	return out
}

func repositoryOf(m *relicv1.Repository) (repository.Repository, error) {
	if m == nil {
		return repository.Repository{}, fmt.Errorf("%w: repository is required", repository.ErrInvalidRepository)
	}

	class, ok := classes[m.GetClass()]
	if !ok {
		return repository.Repository{}, fmt.Errorf("%w: class is required", repository.ErrInvalidRepository)
	}

	format, err := formatOf(m.GetFormat())
	if err != nil {
		return repository.Repository{}, err
	}

	r := repository.Repository{
		Name:    m.GetName(),
		Class:   class,
		Format:  format,
		Project: m.GetProject(),
		Config: repository.Config{
			Description:         m.GetDescription(),
			Notes:               m.GetNotes(),
			Environments:        m.GetEnvironments(),
			SigningKey:          m.GetSigningKey(),
			SecondarySigningKey: m.GetSecondarySigningKey(),
			Architectures:       m.GetArchitectures(),
			MetadataDepth:       m.GetMetadataDepth(),
			MaxUploadSize:       m.GetMaxUploadSize(),
			MaxStorage:          m.GetMaxStorage(),
			Immutable:           m.GetImmutable(),
		},
	}
	if rm := m.GetRemote(); rm != nil {
		r.Config.Remote = &repository.RemoteConfig{
			URL:                 rm.GetUrl(),
			Username:            rm.GetUsername(),
			Password:            rm.GetPassword(),
			Offline:             rm.GetOffline(),
			DisableCache:        rm.GetDisableCache(),
			CachePeriodSeconds:  rm.GetCachePeriodSeconds(),
			TimeoutMilliseconds: rm.GetTimeoutMilliseconds(),
			IndexURL:            rm.GetIndexUrl(),

			MissedRetrievalCachePeriodSeconds: rm.MissedRetrievalCachePeriodSeconds,
			CooldownDays:                      rm.GetCooldownDays(),
			Quarantine:                        rm.GetQuarantine(),
		}
	}

	if vm := m.GetVirtual(); vm != nil {
		r.Config.Virtual = &repository.VirtualConfig{
			Members:           vm.GetMembers(),
			DeployTo:          vm.GetDeployTo(),
			LocalOnlyPatterns: vm.GetLocalOnlyPatterns(),
		}
	}

	return r, nil
}

func (s *repositoryServer) canRead(ctx context.Context, r repository.Repository) (bool, error) {
	p := auth.FromContext(ctx)
	if p.Admin || p.Viewer {
		return true, nil
	}

	perms, err := s.d.Security.Permissions(ctx)
	if err != nil {
		return false, err
	}

	return perms.CanReadRepository(p.Subject(), auth.Repository{
		Name:  r.Name,
		Class: string(r.Class),
	}), nil
}

func (s *repositoryServer) ListRepositories(
	ctx context.Context,
	req *relicv1.ListRepositoriesRequest,
) (*relicv1.ListRepositoriesResponse, error) {
	format, err := formatOf(req.GetFormat())
	if err != nil {
		return nil, err
	}

	class, ok := classes[req.GetClass()]
	if !ok && req.GetClass() != relicv1.RepositoryClass_REPOSITORY_CLASS_UNSPECIFIED {
		return nil, connect.Errorf(
			connect.CodeInvalidArgument,
			"unknown repository class %d", req.GetClass(),
		)
	}

	list, err := s.d.Repositories.ListRepositories(ctx, class, format)
	if err != nil {
		return nil, err
	}

	out := &relicv1.ListRepositoriesResponse{
		Repositories: make([]*relicv1.Repository, 0, len(list)),
	}
	for _, r := range list {
		ok, err := s.canRead(ctx, r)
		if err != nil {
			return nil, err
		}

		if ok {
			out.Repositories = append(out.Repositories, RepositoryMessage(r))
		}
	}

	return out, nil
}

func (s *repositoryServer) GetRepository(
	ctx context.Context,
	req *relicv1.GetRepositoryRequest,
) (*relicv1.GetRepositoryResponse, error) {
	r, err := s.d.Repositories.GetRepository(ctx, req.GetName())
	if err != nil {
		return nil, err
	}

	ok, err := s.canRead(ctx, r)
	if err != nil {
		return nil, err
	}

	if !ok {
		return nil, repository.ErrRepositoryNotFound
	}

	m := RepositoryMessage(r)
	if m.UsedBytes, err = s.d.Repositories.UsedBytes(ctx, r.Name); err != nil {
		return nil, err
	}

	canUpload, canDelete, err := s.callerRights(ctx, r)
	if err != nil {
		return nil, err
	}

	return &relicv1.GetRepositoryResponse{
		Repository: m,
		CanUpload:  canUpload,
		CanDelete:  canDelete,
	}, nil
}

// callerRights reports whether the caller may upload to and delete from some
// path of r; a virtual repository's uploads go to its deploy target.
func (s *repositoryServer) callerRights(
	ctx context.Context,
	r repository.Repository,
) (canUpload, canDelete bool, err error) {
	perms, err := s.d.Security.Permissions(ctx)
	if err != nil {
		return false, false, err
	}

	subject := auth.FromContext(ctx).Subject()
	target := auth.Repository{
		Name:  r.Name,
		Class: string(r.Class),
	}

	switch r.Class {
	case repository.ClassLocal:
		canUpload = perms.CanAnywhereInRepository(subject, target, auth.ActionWrite)
		canDelete = perms.CanAnywhereInRepository(subject, target, auth.ActionDelete)
	case repository.ClassRemote:
		canDelete = perms.CanAnywhereInRepository(subject, target, auth.ActionDelete)
	case repository.ClassVirtual:
		if r.Config.Virtual == nil || r.Config.Virtual.DeployTo == "" {
			return false, false, nil
		}

		deployTo, err := s.d.Repositories.GetRepository(ctx, r.Config.Virtual.DeployTo)
		if errors.Is(err, repository.ErrRepositoryNotFound) {
			return false, false, nil
		}

		if err != nil {
			return false, false, err
		}

		canUpload = perms.CanAnywhereInRepository(subject, auth.Repository{
			Name:  deployTo.Name,
			Class: string(deployTo.Class),
		}, auth.ActionWrite)
	}

	return canUpload, canDelete, nil
}

func (s *repositoryServer) GetRepositorySigningKey(
	ctx context.Context,
	req *relicv1.GetRepositorySigningKeyRequest,
) (*relicv1.GetRepositorySigningKeyResponse, error) {
	r, err := s.d.Repositories.GetRepository(ctx, req.GetName())
	if err != nil {
		return nil, err
	}

	ok, err := s.canRead(ctx, r)
	if err != nil {
		return nil, err
	}

	if !ok {
		return nil, repository.ErrRepositoryNotFound
	}

	keyPair := r.KeyPairRef()
	if keyPair == "" || s.d.SigningKeys == nil {
		return nil, connect.NewError(
			connect.CodeNotFound,
			"repository "+r.Name+" signs no metadata",
		)
	}

	out := &relicv1.GetRepositorySigningKeyResponse{}

	for i, ref := range r.KeyPairRefs() {
		var publicKey string

		switch r.Format {
		case repository.FormatAlpine:
			_, publicKey, err = s.d.SigningKeys.RSA(ctx, ref)
		default:
			_, publicKey, err = s.d.SigningKeys.GPG(ctx, ref)
		}

		if err != nil {
			return nil, err
		}

		if i == 0 {
			out.KeyPair, out.PublicKey = ref, publicKey
		} else {
			out.SecondaryKeyPair, out.SecondaryPublicKey = ref, publicKey
		}
	}

	return out, nil
}

func (s *repositoryServer) GetClientEndpoints(
	ctx context.Context,
	req *relicv1.GetClientEndpointsRequest,
) (*relicv1.GetClientEndpointsResponse, error) {
	r, err := s.d.Repositories.GetRepository(ctx, req.GetName())
	if err != nil {
		return nil, err
	}

	ok, err := s.canRead(ctx, r)
	if err != nil {
		return nil, err
	}

	if !ok {
		return nil, repository.ErrRepositoryNotFound
	}

	origin := requestOrigin(httpRequest(ctx))

	out := &relicv1.GetClientEndpointsResponse{
		Files: origin + "/api/v1/files/" + url.PathEscape(r.Name) + "/",
	}
	if s.d.ClientEndpoints != nil {
		out.PackageManager, out.ContainerRegistry = s.d.ClientEndpoints(origin, r)
	}

	return out, nil
}

func requestOrigin(r *http.Request) string {
	if r == nil {
		return ""
	}

	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}

	return scheme + "://" + r.Host
}

func (s *repositoryServer) CreateRepository(
	ctx context.Context,
	req *relicv1.CreateRepositoryRequest,
) (*relicv1.CreateRepositoryResponse, error) {
	r, err := repositoryOf(req.GetRepository())
	if err != nil {
		return nil, err
	}

	if err := administersProject(ctx, s.d, r.Project); err != nil {
		return nil, err
	}

	if err := readableMembers(ctx, s.d, r, nil); err != nil {
		return nil, err
	}

	if err := s.d.Repositories.CreateRepository(ctx, r); err != nil {
		return nil, err
	}

	s.d.Security.Invalidate()

	created, err := s.d.Repositories.GetRepository(ctx, r.Name)
	if err != nil {
		return nil, err
	}

	return &relicv1.CreateRepositoryResponse{
		Repository: RepositoryMessage(created),
	}, nil
}

func (s *repositoryServer) UpdateRepository(
	ctx context.Context,
	req *relicv1.UpdateRepositoryRequest,
) (*relicv1.UpdateRepositoryResponse, error) {
	r, err := repositoryOf(req.GetRepository())
	if err != nil {
		return nil, err
	}

	existing, err := readableRepository(ctx, s.d, r.Name)
	if err != nil {
		return nil, err
	}

	if err := administersProject(ctx, s.d, existing.Project); err != nil {
		return nil, err
	}

	if err := administersProject(ctx, s.d, r.Project); err != nil {
		return nil, err
	}

	if err := readableMembers(ctx, s.d, r, existing.Members()); err != nil {
		return nil, err
	}

	r.Config.Extensions = existing.Config.Extensions
	if err := s.d.Repositories.UpdateRepository(ctx, r); err != nil {
		return nil, err
	}

	s.d.Security.Invalidate()

	updated, err := s.d.Repositories.GetRepository(ctx, r.Name)
	if err != nil {
		return nil, err
	}

	return &relicv1.UpdateRepositoryResponse{
		Repository: RepositoryMessage(updated),
	}, nil
}

func readableMembers(ctx context.Context, d Dependencies, r repository.Repository, kept []string) error {
	for _, member := range r.Members() {
		if slices.Contains(kept, member) {
			continue
		}

		_, err := readableRepository(ctx, d, member)
		if errors.Is(err, repository.ErrRepositoryNotFound) {
			return fmt.Errorf("%w: member %s of %s does not exist", repository.ErrInvalidRepository, member, r.Name)
		}

		if err != nil {
			return err
		}
	}

	return nil
}

func (s *repositoryServer) DeleteRepository(
	ctx context.Context,
	req *relicv1.DeleteRepositoryRequest,
) (*relicv1.DeleteRepositoryResponse, error) {
	existing, err := readableRepository(ctx, s.d, req.GetName())
	if err != nil {
		return nil, err
	}

	if err := administersProject(ctx, s.d, existing.Project); err != nil {
		return nil, err
	}

	if err := s.d.Repositories.DeleteRepository(ctx, req.GetName()); err != nil {
		return nil, err
	}

	s.d.Security.Invalidate()

	return &relicv1.DeleteRepositoryResponse{}, nil
}
