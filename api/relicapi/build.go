package relicapi

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/access/auth"
	"relic/internal/artifacts/repository"
	"relic/internal/delivery/buildinfo"
)

type buildServer struct {
	d Dependencies
}

func (s *buildServer) canRead(ctx context.Context, project, name string) (bool, error) {
	p := auth.FromContext(ctx)
	if p.Admin {
		return true, nil
	}

	perms, err := s.d.Security.Permissions(ctx)
	if err != nil {
		return false, err
	}

	return perms.CanBuild(p.Subject(), s.d.Builds.Repository(project), name, auth.ActionRead), nil
}

func (s *buildServer) ListBuilds(
	ctx context.Context,
	req *connect.Request[relicv1.ListBuildsRequest],
) (*connect.Response[relicv1.ListBuildsResponse], error) {
	list, err := s.d.Builds.List(ctx, req.Msg.GetProject())
	if err != nil {
		return nil, err
	}

	out := &relicv1.ListBuildsResponse{
		Builds: make([]*relicv1.Build, 0, len(list)),
	}
	for _, b := range list {
		ok, err := s.canRead(ctx, req.Msg.GetProject(), b.Name)
		if err != nil {
			return nil, err
		}

		if ok {
			out.Builds = append(out.Builds, &relicv1.Build{
				Name:        b.Name,
				LastStarted: timestamppb.New(b.LastStarted),
			})
		}
	}

	return connect.NewResponse(out), nil
}

func (s *buildServer) ListBuildRuns(
	ctx context.Context,
	req *connect.Request[relicv1.ListBuildRunsRequest],
) (*connect.Response[relicv1.ListBuildRunsResponse], error) {
	ok, err := s.canRead(ctx, req.Msg.GetProject(), req.Msg.GetName())
	if err != nil {
		return nil, err
	}

	if !ok {
		return nil, buildinfo.ErrNotFound
	}

	runs, err := s.d.Builds.Runs(ctx, req.Msg.GetProject(), req.Msg.GetName())
	if err != nil {
		return nil, err
	}

	if len(runs) == 0 {
		return nil, buildinfo.ErrNotFound
	}

	out := &relicv1.ListBuildRunsResponse{
		Runs: make([]*relicv1.BuildRun, 0, len(runs)),
	}
	for _, r := range runs {
		out.Runs = append(out.Runs, &relicv1.BuildRun{
			Number:  r.Number,
			Started: timestamppb.New(r.Started),
		})
	}

	return connect.NewResponse(out), nil
}

func (s *buildServer) canBuild(ctx context.Context, project, name string, a auth.Action) error {
	ok, err := s.canRead(ctx, project, name)
	if err != nil {
		return err
	}

	if !ok {
		return buildinfo.ErrNotFound
	}

	if a == auth.ActionRead || auth.FromContext(ctx).Admin {
		return nil
	}

	perms, err := s.d.Security.Permissions(ctx)
	if err != nil {
		return err
	}

	if !perms.CanBuild(auth.FromContext(ctx).Subject(), s.d.Builds.Repository(project), name, a) {
		return connect.NewError(connect.CodePermissionDenied, errors.New("no "+string(a)+" permission on build "+name))
	}

	return nil
}

func (s *buildServer) itemCheck(ctx context.Context) buildinfo.ItemCheck {
	return func(it repository.Item, a auth.Action) bool {
		return can(ctx, s.d, it.Repository, it.Path(), a) == nil
	}
}

func (s *buildServer) DeleteBuildRuns(
	ctx context.Context,
	req *connect.Request[relicv1.DeleteBuildRunsRequest],
) (*connect.Response[relicv1.DeleteBuildRunsResponse], error) {
	m := req.Msg
	if err := s.canBuild(ctx, m.GetProject(), m.GetName(), auth.ActionDelete); err != nil {
		return nil, err
	}

	if !m.GetAll() && len(m.GetNumbers()) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name the runs to delete, or set all"))
	}

	if err := s.d.Builds.Delete(
		ctx,
		m.GetProject(),
		m.GetName(),
		m.GetNumbers(),
		m.GetAll(),
		m.GetDeleteArtifacts(),
		s.itemCheck(ctx),
	); err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.DeleteBuildRunsResponse{}), nil
}

func (s *buildServer) ApplyBuildRetention(
	ctx context.Context,
	req *connect.Request[relicv1.ApplyBuildRetentionRequest],
) (*connect.Response[relicv1.ApplyBuildRetentionResponse], error) {
	m := req.Msg
	if err := s.canBuild(ctx, m.GetProject(), m.GetName(), auth.ActionDelete); err != nil {
		return nil, err
	}

	if _, err := s.d.Builds.Runs(ctx, m.GetProject(), m.GetName()); err != nil {
		return nil, err
	}

	ret := buildinfo.Retention{
		MaxRuns:         int(m.GetMaxRuns()),
		Keep:            m.GetKeep(),
		DeleteArtifacts: m.GetDeleteArtifacts(),
	}
	if m.GetMinStarted() != nil {
		ret.MinStarted = m.GetMinStarted().AsTime()
	}

	if err := s.d.Builds.Discard(ctx, m.GetProject(), m.GetName(), ret, s.itemCheck(ctx)); err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.ApplyBuildRetentionResponse{}), nil
}

func (s *buildServer) PromoteBuildRun(
	ctx context.Context,
	req *connect.Request[relicv1.PromoteBuildRunRequest],
) (*connect.Response[relicv1.PromoteBuildRunResponse], error) {
	m := req.Msg
	if err := s.canBuild(ctx, m.GetProject(), m.GetName(), auth.ActionWrite); err != nil {
		return nil, err
	}

	if t := m.GetTargetRepository(); t != "" {
		if err := can(ctx, s.d, t, "", auth.ActionWrite); err != nil {
			return nil, err
		}
	}

	props := map[string][]string{}
	for k, v := range m.GetProperties() {
		props[k] = v.GetValues()
	}

	msgs, err := s.d.Builds.Promote(ctx, m.GetProject(), m.GetName(), m.GetNumber(), buildinfo.Promotion{
		Status:           m.GetStatus(),
		Comment:          m.GetComment(),
		SourceRepository: m.GetSourceRepository(),
		TargetRepository: m.GetTargetRepository(),
		Dependencies:     m.GetDependencies(),
		Copy:             m.GetCopy(),
		FailFast:         m.GetFailFast(),
		DryRun:           m.GetDryRun(),
		Properties:       props,
	}, auth.FromContext(ctx).Name(), s.itemCheck(ctx))

	out := &relicv1.PromoteBuildRunResponse{
		Messages: make([]*relicv1.PromotionMessage, 0, len(msgs)),
	}
	for _, msg := range msgs {
		out.Messages = append(
			out.Messages,
			&relicv1.PromotionMessage{
				Level: strings.ToLower(string(msg.Level)),
				Text:  msg.Text,
			},
		)
	}

	if err != nil && len(out.GetMessages()) > 0 {
		ce := codeOf(err)
		if ce.Code() == connect.CodeInternal {
			s.d.Log.ErrorContext(ctx, "relic api: promote build run", "error", err)
		}

		if detail, derr := connect.NewErrorDetail(out); derr == nil {
			ce.AddDetail(detail)
		}

		return nil, ce
	}

	if err != nil {
		return nil, err
	}

	return connect.NewResponse(out), nil
}

func (s *buildServer) GetBuildRun(
	ctx context.Context,
	req *connect.Request[relicv1.GetBuildRunRequest],
) (*connect.Response[relicv1.GetBuildRunResponse], error) {
	m := req.Msg

	ok, err := s.canRead(ctx, m.GetProject(), m.GetName())
	if err != nil {
		return nil, err
	}

	if !ok {
		return nil, buildinfo.ErrNotFound
	}

	run, err := s.d.Builds.Run(ctx, m.GetProject(), m.GetName(), m.GetNumber())
	if err != nil {
		return nil, err
	}

	b := run.Document
	out := &relicv1.GetBuildRunResponse{
		Name:       b.Name,
		Number:     b.Number,
		Properties: b.Properties,
		Started:    timestamppb.New(b.Started),
	}

	out.Agent = strings.TrimSpace(b.Agent.Name + " " + b.Agent.Version)
	for _, v := range b.VCS {
		out.Vcs = append(out.Vcs, &relicv1.VcsSource{
			Url:      v.URL,
			Revision: v.Revision,
			Branch:   v.Branch,
		})
	}

	for _, mod := range b.Modules {
		bm := &relicv1.BuildModule{
			Id:   mod.ID,
			Type: mod.Type,
		}
		for _, a := range mod.Artifacts {
			bm.Artifacts = append(
				bm.Artifacts,
				&relicv1.BuildArtifact{
					Name:   a.Name,
					Path:   a.Path,
					Type:   a.Type,
					Sha1:   a.SHA1,
					Sha256: a.SHA256,
					Md5:    a.MD5,
				},
			)
		}

		for _, dep := range mod.Dependencies {
			bm.Dependencies = append(
				bm.Dependencies,
				&relicv1.BuildDependency{
					Id:     dep.ID,
					Type:   dep.Type,
					Sha1:   dep.SHA1,
					Sha256: dep.SHA256,
					Md5:    dep.MD5,
					Scopes: dep.Scopes,
				},
			)
		}

		out.Modules = append(out.Modules, bm)
	}

	return connect.NewResponse(out), nil
}

func (s *buildServer) PublishBuildRun(
	ctx context.Context,
	req *connect.Request[relicv1.PublishBuildRunRequest],
) (*connect.Response[relicv1.PublishBuildRunResponse], error) {
	run := req.Msg.GetRun()
	if run.GetName() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("run.name is required"))
	}

	p := auth.FromContext(ctx)
	if !p.Admin {
		perms, err := s.d.Security.Permissions(ctx)
		if err != nil {
			return nil, err
		}

		if !perms.CanBuild(
			p.Subject(),
			s.d.Builds.Repository(req.Msg.GetProject()),
			run.GetName(),
			auth.ActionWrite,
		) {
			return nil, connect.NewError(
				connect.CodePermissionDenied,
				errors.New("no write permission on build "+run.GetName()),
			)
		}
	}

	if err := s.d.Builds.PublishDocument(ctx, req.Msg.GetProject(), documentOf(run), p.Name()); err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.PublishBuildRunResponse{}), nil
}

func documentOf(m *relicv1.PublishedBuildRun) buildinfo.Document {
	doc := buildinfo.Document{
		Name:       m.GetName(),
		Number:     m.GetNumber(),
		Properties: m.GetProperties(),
		Agent: buildinfo.Agent{
			Name:    m.GetAgentName(),
			Version: m.GetAgentVersion(),
		},
	}
	if m.GetStarted() != nil {
		doc.Started = m.GetStarted().AsTime()
	}

	for _, v := range m.GetVcs() {
		doc.VCS = append(doc.VCS, buildinfo.VCS{
			URL:      v.GetUrl(),
			Revision: v.GetRevision(),
			Branch:   v.GetBranch(),
		})
	}

	for _, mod := range m.GetModules() {
		out := buildinfo.Module{
			ID:   mod.GetId(),
			Type: mod.GetType(),
		}
		for _, a := range mod.GetArtifacts() {
			out.Artifacts = append(out.Artifacts, buildinfo.Artifact{
				Name:   a.GetName(),
				Type:   a.GetType(),
				Path:   a.GetPath(),
				SHA1:   a.GetSha1(),
				SHA256: a.GetSha256(),
				MD5:    a.GetMd5(),
			})
		}

		for _, d := range mod.GetDependencies() {
			out.Dependencies = append(out.Dependencies, buildinfo.Dependency{
				ID:     d.GetId(),
				Type:   d.GetType(),
				Scopes: d.GetScopes(),
				SHA1:   d.GetSha1(),
				SHA256: d.GetSha256(),
				MD5:    d.GetMd5(),
			})
		}

		doc.Modules = append(doc.Modules, out)
	}

	return doc
}
