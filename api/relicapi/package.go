package relicapi

import (
	"cmp"
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/access/auth"
	"relic/internal/artifacts/formats/docker"
	"relic/internal/artifacts/formats/npm"
	"relic/internal/artifacts/formats/pypi"
	"relic/internal/artifacts/repository"
)

type packageServer struct {
	d Dependencies
}

func packageFile(ctx context.Context, d Dependencies, key, p string) (repository.Repository, repository.Item, error) {
	if err := can(ctx, d, key, p, auth.ActionRead); err != nil {
		return repository.Repository{}, repository.Item{}, err
	}

	r, err := d.Repositories.GetRepository(ctx, key)
	if err != nil {
		return repository.Repository{}, repository.Item{}, err
	}

	it, err := d.Repositories.Stat(ctx, key, p)

	return r, it, err
}

func notPackage(what string) error {
	return connect.NewError(connect.CodeNotFound, errors.New("not "+what))
}

func (s *packageServer) GetContainerImage(
	ctx context.Context,
	req *connect.Request[relicv1.GetContainerImageRequest],
) (*connect.Response[relicv1.GetContainerImageResponse], error) {
	key, p := req.Msg.GetRepository(), cleanPath(req.Msg.GetPath())
	if err := can(ctx, s.d, key, p, auth.ActionRead); err != nil {
		return nil, err
	}

	info, err := s.d.ContainerImages.ImageInfo(ctx, key, p)
	if errors.Is(err, docker.ErrNotImage) {
		return nil, notPackage("an image")
	}

	if err != nil {
		return nil, err
	}

	out := &relicv1.GetContainerImageResponse{
		Digest:       info.Digest,
		MediaType:    info.MediaType,
		Os:           info.OS,
		Architecture: info.Architecture,
		Created:      ts(info.Created),
		TotalSize:    info.TotalSize,
		Labels:       info.Labels,
	}
	for _, l := range info.Layers {
		out.Layers = append(
			out.Layers,
			&relicv1.ImageLayer{
				Digest:      l.Digest,
				Size:        l.Size,
				MediaType:   l.MediaType,
				Instruction: l.Instruction,
				Created:     ts(l.Created),
			},
		)
	}

	for _, pl := range info.Platforms {
		out.Platforms = append(
			out.Platforms,
			&relicv1.ImagePlatform{
				Digest:       pl.Digest,
				Os:           pl.OS,
				Architecture: pl.Architecture,
				Variant:      pl.Variant,
				Size:         pl.Size,
			},
		)
	}

	return connect.NewResponse(out), nil
}

func (s *packageServer) GetNpmPackage(
	ctx context.Context,
	req *connect.Request[relicv1.GetNpmPackageRequest],
) (*connect.Response[relicv1.GetNpmPackageResponse], error) {
	_, it, err := packageFile(ctx, s.d, req.Msg.GetRepository(), cleanPath(req.Msg.GetPath()))
	if err != nil {
		return nil, err
	}

	if it.IsFolder() || !strings.HasSuffix(it.Name, ".tgz") {
		return nil, notPackage("an npm package")
	}

	info, err := s.d.NpmPackages.PackageInfo(ctx, it)
	if errors.Is(err, npm.ErrInvalid) {
		return nil, notPackage("an npm package")
	}

	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.GetNpmPackageResponse{
		Name:             info.Name,
		Version:          info.Version,
		Description:      info.Description,
		License:          info.License,
		Homepage:         info.Homepage,
		Repository:       info.Repository,
		Keywords:         info.Keywords,
		Dependencies:     info.Dependencies,
		DevDependencies:  info.DevDependencies,
		PeerDependencies: info.PeerDependencies,
	}), nil
}

func (s *packageServer) GetPythonPackage(
	ctx context.Context,
	req *connect.Request[relicv1.GetPythonPackageRequest],
) (*connect.Response[relicv1.GetPythonPackageResponse], error) {
	r, it, err := packageFile(ctx, s.d, req.Msg.GetRepository(), cleanPath(req.Msg.GetPath()))
	if err != nil {
		return nil, err
	}

	if it.IsFolder() || r.Format != repository.FormatPyPI {
		return nil, notPackage("a Python package")
	}

	m, err := s.d.PythonPackages.Metadata(ctx, it)
	if errors.Is(err, pypi.ErrNoMetadata) {
		return nil, notPackage("a Python package")
	}

	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("cannot read the package metadata"))
	}

	return connect.NewResponse(&relicv1.GetPythonPackageResponse{
		Name:           m.Name,
		Version:        m.Version,
		Summary:        m.Summary,
		License:        m.License,
		RequiresPython: m.RequiresPython,
		Author:         m.Author,
		HomePage:       m.HomePage,
		ProjectUrls:    m.ProjectURLs,
		RequiresDist:   m.RequiresDist,
		Classifiers:    m.Classifiers,
	}), nil
}

func (s *packageServer) GetHelmChart(
	ctx context.Context,
	req *connect.Request[relicv1.GetHelmChartRequest],
) (*connect.Response[relicv1.GetHelmChartResponse], error) {
	r, it, err := packageFile(ctx, s.d, req.Msg.GetRepository(), cleanPath(req.Msg.GetPath()))
	if err != nil {
		return nil, err
	}

	if it.IsFolder() || r.Format != repository.FormatHelm || !strings.HasSuffix(it.Name, ".tgz") {
		return nil, notPackage("a Helm chart")
	}

	c, err := s.d.HelmCharts.ChartInfo(ctx, it)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("cannot read Chart.yaml"))
	}

	named := func(in []map[string]string) []*relicv1.PackageMetadataEntry {
		out := make([]*relicv1.PackageMetadataEntry, 0, len(in))
		for _, m := range in {
			out = append(out, &relicv1.PackageMetadataEntry{
				Fields: m,
			})
		}

		return out
	}

	return connect.NewResponse(&relicv1.GetHelmChartResponse{
		Name:         c.Name,
		Version:      c.Version,
		AppVersion:   c.AppVersion,
		Description:  c.Description,
		Type:         c.Type,
		ApiVersion:   c.APIVersion,
		KubeVersion:  c.KubeVersion,
		Home:         c.Home,
		Icon:         c.Icon,
		Deprecated:   c.Deprecated,
		Keywords:     c.Keywords,
		Sources:      c.Sources,
		Maintainers:  named(c.Maintainers),
		Dependencies: named(c.Dependencies),
	}), nil
}

func (s *packageServer) PromoteContainerImage(
	ctx context.Context,
	req *connect.Request[relicv1.PromoteContainerImageRequest],
) (*connect.Response[relicv1.PromoteContainerImageResponse], error) {
	m := req.Msg
	image := strings.Trim(m.GetImage(), "/")
	targetImage := cmp.Or(strings.Trim(m.GetTargetImage(), "/"), image)

	if !docker.ValidImage(image) || !docker.ValidImage(targetImage) ||
		(m.GetTag() != "" && !docker.ValidTag(m.GetTag())) ||
		(m.GetTargetTag() != "" && !docker.ValidTag(m.GetTargetTag())) || m.GetTargetRepository() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid image, tag or target repository"))
	}

	sourceAction := auth.ActionDelete
	if m.GetCopy() {
		sourceAction = auth.ActionRead
	}

	if err := can(ctx, s.d, m.GetRepository(), image, sourceAction); err != nil {
		return nil, err
	}

	if err := can(ctx, s.d, m.GetTargetRepository(), targetImage, auth.ActionWrite); err != nil {
		return nil, err
	}

	err := s.d.ContainerImages.Promote(ctx, docker.PromoteInput{
		SourceRepository: m.GetRepository(),
		TargetRepository: m.GetTargetRepository(),
		Image:            image,
		TargetImage:      targetImage,
		Tag:              m.GetTag(),
		TargetTag:        m.GetTargetTag(),
		Copy:             m.GetCopy(),
		User:             auth.FromContext(ctx).Name(),
	})
	if errors.Is(err, docker.ErrManifestUnknown) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such image tag"))
	}

	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.PromoteContainerImageResponse{}), nil
}
