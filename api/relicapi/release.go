package relicapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"slices"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/access/auth"
	"relic/internal/delivery/release"
)

// ReleaseImportPath imports a release archive: POST ReleaseImportPath with the archive as the body.
const ReleaseImportPath = "/api/v1/releases/import"

const maxReleaseArchive = 32 << 30

type releaseServer struct {
	d Dependencies
}

func (s *releaseServer) canRelease(ctx context.Context, project, name, version string, a auth.Action) error {
	p := auth.FromContext(ctx)
	if p.Admin {
		return nil
	}

	perms, err := s.d.Security.Permissions(ctx)
	if err != nil {
		return err
	}

	repository := s.d.Releases.Layout().Repository(project)
	if !perms.CanRelease(p.Subject(), repository, name, version, auth.ActionRead) {
		return release.ErrNotFound
	}

	if a != auth.ActionRead && !perms.CanRelease(p.Subject(), repository, name, version, a) {
		return connect.Errorf(connect.CodePermissionDenied, "the release permission does not grant %s", a)
	}

	return nil
}

func (s *releaseServer) access(ctx context.Context) (release.Access, error) {
	p := auth.FromContext(ctx)
	if p.Admin {
		return release.Access{}, nil
	}

	perms, err := s.d.Security.Permissions(ctx)
	if err != nil {
		return release.Access{}, err
	}

	local := func(key string) auth.Repository {
		return auth.Repository{
			Name:  key,
			Class: "local",
		}
	}

	return release.Access{
		CanRead: func(r, path string) bool {
			return perms.CanInRepository(p.Subject(), local(r), path, auth.ActionRead)
		},
		CanWrite: func(r, path string) bool {
			return perms.CanInRepository(p.Subject(), local(r), path, auth.ActionWrite)
		},
		CanDelete: func(r, path string) bool {
			return perms.CanInRepository(p.Subject(), local(r), path, auth.ActionDelete)
		},
		CanReadBuild: func(r, name string) bool {
			return perms.CanBuild(p.Subject(), r, name, auth.ActionRead)
		},
	}, nil
}

func (s *releaseServer) readable(ctx context.Context, all []release.Release) ([]release.Release, error) {
	var out []release.Release

	for _, r := range all {
		switch err := s.canRelease(ctx, r.Project, r.Name, r.Version, auth.ActionRead); {
		case err == nil:
			out = append(out, r)
		case errors.Is(err, release.ErrNotFound):
		default:
			return nil, err
		}
	}

	return out, nil
}

var releaseStatuses = map[release.Status]relicv1.ReleaseStatus{
	release.StatusProcessing: relicv1.ReleaseStatus_RELEASE_STATUS_PROCESSING,
	release.StatusCompleted:  relicv1.ReleaseStatus_RELEASE_STATUS_COMPLETED,
	release.StatusDraft:      relicv1.ReleaseStatus_RELEASE_STATUS_DRAFT,
	release.StatusFailed:     relicv1.ReleaseStatus_RELEASE_STATUS_FAILED,
}

func releaseMessage(r release.Release, manifest string) *relicv1.Release {
	project := r.Project
	if project == release.DefaultProject {
		project = ""
	}

	out := &relicv1.Release{
		Name:       r.Name,
		Version:    r.Version,
		Project:    project,
		Repository: r.Repository,
		Status:     releaseStatuses[r.Status],
		Tag:        r.Tag,
		CreatedBy:  r.CreatedBy,
		Created:    timestamppb.New(r.Created),
		Versions:   r.Versions,
		Manifest:   manifest,
	}
	for _, a := range r.Artifacts {
		out.Artifacts = append(
			out.Artifacts,
			&relicv1.ReleaseArtifact{
				Repository: a.Repository,
				Path:       a.Path,
				Sha256:     a.SHA256,
				Size:       a.Size,
			},
		)
	}

	return out
}

func promotionMessage(p release.Promotion) *relicv1.ReleasePromotion {
	return &relicv1.ReleasePromotion{
		Status:        releaseStatuses[p.Status],
		Environment:   p.Environment,
		Include:       p.Include,
		Exclude:       p.Exclude,
		Messages:      p.Messages,
		CreatedBy:     p.CreatedBy,
		Created:       timestamppb.New(p.Created),
		CreatedMillis: p.CreatedMillis,
	}
}

func specOf(m *relicv1.ReleaseSpec) (release.Spec, error) {
	if m == nil {
		return release.Spec{}, fmt.Errorf("%w: spec is required", release.ErrInvalid)
	}

	spec := release.Spec{
		Name:    m.GetName(),
		Version: m.GetVersion(),
		Draft:   m.GetDraft(),
	}
	for _, src := range m.GetSources() {
		var s release.Source

		set := 0

		if src.GetQuery() != nil {
			q, err := itemQueryOf(src.GetQuery())
			if err != nil {
				return release.Spec{}, fmt.Errorf("%w: %w", release.ErrInvalid, err)
			}

			s.Kind, s.Query = release.SourceQuery, q
			set++
		}

		if len(src.GetFiles()) > 0 {
			s.Kind = release.SourceArtifacts
			for _, f := range src.GetFiles() {
				s.Artifacts = append(s.Artifacts, release.ArtifactRef{
					Path:   f.GetPath(),
					SHA256: f.GetSha256(),
				})
			}

			set++
		}

		if len(src.GetBuilds()) > 0 {
			s.Kind = release.SourceBuilds
			for _, b := range src.GetBuilds() {
				s.Builds = append(
					s.Builds,
					release.BuildRef{
						Name:         b.GetName(),
						Number:       b.GetNumber(),
						Project:      b.GetProject(),
						Dependencies: b.GetDependencies(),
					},
				)
			}

			set++
		}

		if len(src.GetReleases()) > 0 {
			s.Kind = release.SourceReleases
			for _, r := range src.GetReleases() {
				s.Releases = append(
					s.Releases,
					release.Ref{
						Name:    r.GetName(),
						Version: r.GetVersion(),
						Project: r.GetProject(),
					},
				)
			}

			set++
		}

		if set != 1 {
			return release.Spec{}, fmt.Errorf(
				"%w: a source sets exactly one of query, files, builds or releases",
				release.ErrInvalid,
			)
		}

		spec.Sources = append(spec.Sources, s)
	}

	return spec, nil
}

func (s *releaseServer) ListReleases(
	ctx context.Context,
	req *relicv1.ListReleasesRequest,
) (*relicv1.ListReleasesResponse, error) {
	all, err := s.d.Releases.Groups(ctx)
	if err != nil {
		return nil, err
	}

	if req.Project != nil {
		stored := s.d.Releases.Layout().Repository(req.GetProject())
		all = slices.DeleteFunc(all, func(r release.Release) bool {
			return r.Repository != stored
		})
	}

	if all, err = s.readable(ctx, all); err != nil {
		return nil, err
	}

	out := &relicv1.ListReleasesResponse{
		Releases: make([]*relicv1.Release, 0, len(all)),
	}
	for _, r := range all {
		out.Releases = append(out.Releases, releaseMessage(r, s.d.Releases.ManifestPath(r)))
	}

	return out, nil
}

func (s *releaseServer) ListReleaseVersions(
	ctx context.Context,
	req *relicv1.ListReleaseVersionsRequest,
) (*relicv1.ListReleaseVersionsResponse, error) {
	all, err := s.d.Releases.Versions(ctx, req.GetProject(), req.GetName())
	if err != nil {
		return nil, err
	}

	if all, err = s.readable(ctx, all); err != nil {
		return nil, err
	}

	out := &relicv1.ListReleaseVersionsResponse{
		Releases: make([]*relicv1.Release, 0, len(all)),
	}
	for _, r := range all {
		out.Releases = append(out.Releases, releaseMessage(r, s.d.Releases.ManifestPath(r)))
	}

	return out, nil
}

func (s *releaseServer) GetRelease(
	ctx context.Context,
	req *relicv1.GetReleaseRequest,
) (*relicv1.GetReleaseResponse, error) {
	m := req
	if err := s.canRelease(ctx, m.GetProject(), m.GetName(), m.GetVersion(), auth.ActionRead); err != nil {
		return nil, err
	}

	r, err := s.d.Releases.Get(ctx, m.GetProject(), m.GetName(), m.GetVersion())
	if err != nil {
		return nil, err
	}

	return &relicv1.GetReleaseResponse{
		Release: releaseMessage(r, s.d.Releases.ManifestPath(r)),
	}, nil
}

func (s *releaseServer) write(ctx context.Context, project string, m *relicv1.ReleaseSpec,
	op func(context.Context, string, release.Spec, string, release.Access) (release.Release, error),
) (*relicv1.Release, error) {
	spec, err := specOf(m)
	if err != nil {
		return nil, err
	}

	if err := s.canRelease(ctx, project, spec.Name, spec.Version, auth.ActionWrite); err != nil {
		return nil, err
	}

	access, err := s.access(ctx)
	if err != nil {
		return nil, err
	}

	r, err := op(ctx, project, spec, auth.FromContext(ctx).Name(), access)
	if err != nil {
		return nil, err
	}

	return releaseMessage(r, s.d.Releases.ManifestPath(r)), nil
}

func (s *releaseServer) CreateRelease(
	ctx context.Context,
	req *relicv1.CreateReleaseRequest,
) (*relicv1.CreateReleaseResponse, error) {
	r, err := s.write(ctx, req.GetProject(), req.GetSpec(), s.d.Releases.Create)
	if err != nil {
		return nil, err
	}

	return &relicv1.CreateReleaseResponse{
		Release: r,
	}, nil
}

func (s *releaseServer) UpdateRelease(
	ctx context.Context,
	req *relicv1.UpdateReleaseRequest,
) (*relicv1.UpdateReleaseResponse, error) {
	r, err := s.write(ctx, req.GetProject(), req.GetSpec(), s.d.Releases.Update)
	if err != nil {
		return nil, err
	}

	return &relicv1.UpdateReleaseResponse{
		Release: r,
	}, nil
}

func (s *releaseServer) FinalizeRelease(
	ctx context.Context,
	req *relicv1.FinalizeReleaseRequest,
) (*relicv1.FinalizeReleaseResponse, error) {
	m := req
	if err := s.canRelease(ctx, m.GetProject(), m.GetName(), m.GetVersion(), auth.ActionWrite); err != nil {
		return nil, err
	}

	r, err := s.d.Releases.Finalize(ctx, m.GetProject(), m.GetName(), m.GetVersion(), auth.FromContext(ctx).Name())
	if err != nil {
		return nil, err
	}

	return &relicv1.FinalizeReleaseResponse{
		Release: releaseMessage(r, s.d.Releases.ManifestPath(r)),
	}, nil
}

func (s *releaseServer) DeleteRelease(
	ctx context.Context,
	req *relicv1.DeleteReleaseRequest,
) (*relicv1.DeleteReleaseResponse, error) {
	m := req
	if err := s.canRelease(ctx, m.GetProject(), m.GetName(), m.GetVersion(), auth.ActionDelete); err != nil {
		return nil, err
	}

	if err := s.d.Releases.Delete(ctx, m.GetProject(), m.GetName(), m.GetVersion()); err != nil {
		return nil, err
	}

	return &relicv1.DeleteReleaseResponse{}, nil
}

func (s *releaseServer) SetReleaseTag(
	ctx context.Context,
	req *relicv1.SetReleaseTagRequest,
) (*relicv1.SetReleaseTagResponse, error) {
	m := req
	if err := s.canRelease(ctx, m.GetProject(), m.GetName(), m.GetVersion(), auth.ActionAnnotate); err != nil {
		return nil, err
	}

	if err := s.d.Releases.SetTag(ctx, m.GetProject(), m.GetName(), m.GetVersion(), m.GetTag()); err != nil {
		return nil, err
	}

	return &relicv1.SetReleaseTagResponse{}, nil
}

func (s *releaseServer) PromoteRelease(
	ctx context.Context,
	req *relicv1.PromoteReleaseRequest,
) (*relicv1.PromoteReleaseResponse, error) {
	m := req
	if err := s.canRelease(ctx, m.GetProject(), m.GetName(), m.GetVersion(), auth.ActionPromote); err != nil {
		return nil, err
	}

	access, err := s.access(ctx)
	if err != nil {
		return nil, err
	}

	spec := release.PromotionSpec{
		Environment: m.GetEnvironment(),
		Include:     m.GetInclude(),
		Exclude:     m.GetExclude(),
		Move:        m.GetMove(),
	}

	p, err := s.d.Releases.Promote(
		ctx,
		m.GetProject(),
		m.GetName(),
		m.GetVersion(),
		spec,
		auth.FromContext(ctx).Name(),
		access,
	)
	if err != nil {
		return nil, err
	}

	return &relicv1.PromoteReleaseResponse{
		Promotion: promotionMessage(p),
	}, nil
}

func (s *releaseServer) ListReleasePromotions(
	ctx context.Context,
	req *relicv1.ListReleasePromotionsRequest,
) (*relicv1.ListReleasePromotionsResponse, error) {
	m := req
	if err := s.canRelease(ctx, m.GetProject(), m.GetName(), m.GetVersion(), auth.ActionRead); err != nil {
		return nil, err
	}

	list, err := s.d.Releases.Promotions(ctx, m.GetProject(), m.GetName(), m.GetVersion())
	if err != nil {
		return nil, err
	}

	out := &relicv1.ListReleasePromotionsResponse{
		Promotions: make([]*relicv1.ReleasePromotion, 0, len(list)),
	}
	for _, p := range list {
		out.Promotions = append(out.Promotions, promotionMessage(p))
	}

	return out, nil
}

func (s *releaseServer) DeleteReleasePromotion(
	ctx context.Context,
	req *relicv1.DeleteReleasePromotionRequest,
) (*relicv1.DeleteReleasePromotionResponse, error) {
	m := req
	if err := s.canRelease(ctx, m.GetProject(), m.GetName(), m.GetVersion(), auth.ActionPromote); err != nil {
		return nil, err
	}

	if err := s.d.Releases.DeletePromotion(
		ctx,
		m.GetProject(),
		m.GetName(),
		m.GetVersion(),
		m.GetCreatedMillis(),
	); err != nil {
		return nil, err
	}

	return &relicv1.DeleteReleasePromotionResponse{}, nil
}

func (s *releaseServer) ExportRelease(
	ctx context.Context,
	req *relicv1.ExportReleaseRequest,
) (*relicv1.ExportReleaseResponse, error) {
	m := req
	if err := s.canRelease(ctx, m.GetProject(), m.GetName(), m.GetVersion(), auth.ActionWrite); err != nil {
		return nil, err
	}

	it, err := s.d.Releases.Export(ctx, m.GetProject(), m.GetName(), m.GetVersion(), auth.FromContext(ctx).Name())
	if err != nil {
		return nil, err
	}

	return &relicv1.ExportReleaseResponse{
		Archive: itemMessage(it),
	}, nil
}

func (d Dependencies) importRelease(w http.ResponseWriter, r *http.Request) {
	p := auth.FromContext(r.Context())
	switch {
	case p.Anonymous():
		writeHTTPError(w, connect.NewError(connect.CodeUnauthenticated, "sign in first"))
		return
	case !p.Admin:
		writeHTTPError(
			w,
			connect.NewError(connect.CodePermissionDenied, "importing releases needs an administrator"),
		)

		return
	}

	tmp, err := os.CreateTemp("", "relic-release-import-*")
	if err != nil {
		writeHTTPError(w, d.logged(r, err))
		return
	}

	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()

	size, err := io.Copy(tmp, http.MaxBytesReader(w, r.Body, maxReleaseArchive))
	if _, staging := errors.AsType[*fs.PathError](err); staging {
		writeHTTPError(w, d.logged(r, fmt.Errorf("stage archive: %w", err)))
		return
	}

	if err != nil {
		writeHTTPError(w, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("read archive: %w", err).Error()))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()

	rel, err := d.Releases.Import(ctx, tmp, size, p.Name())
	if err != nil {
		writeHTTPError(w, d.logged(r, err))
		return
	}

	b, err := protojson.Marshal(&relicv1.GetReleaseResponse{
		Release: releaseMessage(rel, d.Releases.ManifestPath(rel)),
	})
	if err != nil {
		writeHTTPError(w, d.logged(r, err))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write(b)
}
