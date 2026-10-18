package relicapi

import (
	"context"
	"slices"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/timestamppb"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/access/auth"
	"relic/internal/access/project"
)

type projectServer struct {
	d Dependencies
}

var projectRoles = map[project.Role]relicv1.ProjectRole{
	project.RoleAdmin:     relicv1.ProjectRole_PROJECT_ROLE_ADMIN,
	project.RoleDeveloper: relicv1.ProjectRole_PROJECT_ROLE_DEVELOPER,
	project.RoleViewer:    relicv1.ProjectRole_PROJECT_ROLE_VIEWER,
}

func domainProjectMembers(ms []*relicv1.ProjectMember) []project.Member {
	out := make([]project.Member, 0, len(ms))

	for _, m := range ms {
		role := project.Role("")

		for r, v := range projectRoles {
			if v == m.GetRole() {
				role = r
			}
		}

		out = append(out, project.Member{
			Name:  m.GetName(),
			Group: m.GetGroup(),
			Role:  role,
		})
	}

	return out
}

func (s *projectServer) projectMessage(
	ctx context.Context,
	p project.Project,
	repos map[string]string,
) (*relicv1.Project, error) {
	used, err := s.d.Repositories.ProjectUsedBytes(ctx, p.Key)
	if err != nil {
		return nil, err
	}

	out := &relicv1.Project{
		Key:               p.Key,
		Name:              p.Name,
		Description:       p.Description,
		StorageQuotaBytes: p.StorageQuotaBytes,
		UsedBytes:         used,
		Created:           timestamppb.New(p.Created),
		Updated:           timestamppb.New(p.Updated),
	}

	for _, m := range p.Members {
		out.Members = append(out.Members, &relicv1.ProjectMember{
			Name:  m.Name,
			Group: m.Group,
			Role:  projectRoles[m.Role],
		})
	}

	for repo, key := range repos {
		if key == p.Key {
			out.Repositories = append(out.Repositories, repo)
		}
	}

	slices.Sort(out.Repositories)

	return out, nil
}

func seesAllProjects(p auth.Principal) bool { return p.Admin || p.Viewer }

func (s *projectServer) ListProjects(
	ctx context.Context,
	_ *relicv1.ListProjectsRequest,
) (*relicv1.ListProjectsResponse, error) {
	list, err := s.d.Projects.List(ctx)
	if err != nil {
		return nil, err
	}

	repos, err := s.d.Repositories.Projects(ctx)
	if err != nil {
		return nil, err
	}

	caller := auth.FromContext(ctx)
	out := &relicv1.ListProjectsResponse{}

	for _, p := range list {
		if !seesAllProjects(caller) && p.Role(caller) == "" {
			continue
		}

		m, err := s.projectMessage(ctx, p, repos)
		if err != nil {
			return nil, err
		}

		out.Projects = append(out.Projects, m)
	}

	return out, nil
}

func (s *projectServer) GetProject(
	ctx context.Context,
	req *relicv1.GetProjectRequest,
) (*relicv1.GetProjectResponse, error) {
	p, err := s.d.Projects.Get(ctx, req.GetKey())
	if err != nil {
		return nil, err
	}

	caller := auth.FromContext(ctx)
	if !seesAllProjects(caller) && p.Role(caller) == "" {
		return nil, connect.NewError(connect.CodeNotFound, project.ErrNotFound.Error()).WithCause(project.ErrNotFound)
	}

	repos, err := s.d.Repositories.Projects(ctx)
	if err != nil {
		return nil, err
	}

	m, err := s.projectMessage(ctx, p, repos)
	if err != nil {
		return nil, err
	}

	return &relicv1.GetProjectResponse{
		Project: m,
	}, nil
}

func (s *projectServer) PutProject(
	ctx context.Context,
	req *relicv1.PutProjectRequest,
) (*relicv1.PutProjectResponse, error) {
	m := req.GetProject()

	p, err := s.d.Projects.Put(ctx, project.Project{
		Key:               m.GetKey(),
		Name:              m.GetName(),
		Description:       m.GetDescription(),
		StorageQuotaBytes: m.GetStorageQuotaBytes(),
		Members:           domainProjectMembers(m.GetMembers()),
	})
	if err != nil {
		return nil, err
	}

	repos, err := s.d.Repositories.Projects(ctx)
	if err != nil {
		return nil, err
	}

	out, err := s.projectMessage(ctx, p, repos)
	if err != nil {
		return nil, err
	}

	return &relicv1.PutProjectResponse{
		Project: out,
	}, nil
}

func (s *projectServer) DeleteProject(
	ctx context.Context,
	req *relicv1.DeleteProjectRequest,
) (*relicv1.DeleteProjectResponse, error) {
	if err := s.d.Projects.Delete(ctx, req.GetKey()); err != nil {
		return nil, err
	}

	return &relicv1.DeleteProjectResponse{}, nil
}

func (s *projectServer) SetProjectMembers(
	ctx context.Context,
	req *relicv1.SetProjectMembersRequest,
) (*relicv1.SetProjectMembersResponse, error) {
	key := req.GetKey()

	if err := administersProject(ctx, s.d, key); err != nil {
		return nil, err
	}

	p, err := s.d.Projects.SetMembers(ctx, key, domainProjectMembers(req.GetMembers()))
	if err != nil {
		return nil, err
	}

	repos, err := s.d.Repositories.Projects(ctx)
	if err != nil {
		return nil, err
	}

	out, err := s.projectMessage(ctx, p, repos)
	if err != nil {
		return nil, err
	}

	return &relicv1.SetProjectMembersResponse{
		Project: out,
	}, nil
}

func (s *projectServer) AddProjectMember(
	ctx context.Context,
	req *relicv1.AddProjectMemberRequest,
) (*relicv1.AddProjectMemberResponse, error) {
	key := req.GetKey()

	if err := administersProject(ctx, s.d, key); err != nil {
		return nil, err
	}

	members := domainProjectMembers([]*relicv1.ProjectMember{
		req.GetMember(),
	})

	p, err := s.d.Projects.AddMember(ctx, key, members[0])
	if err != nil {
		return nil, err
	}

	out, err := s.projectWithRepositories(ctx, p)
	if err != nil {
		return nil, err
	}

	return &relicv1.AddProjectMemberResponse{
		Project: out,
	}, nil
}

func (s *projectServer) RemoveProjectMember(
	ctx context.Context,
	req *relicv1.RemoveProjectMemberRequest,
) (*relicv1.RemoveProjectMemberResponse, error) {
	key := req.GetKey()

	if err := administersProject(ctx, s.d, key); err != nil {
		return nil, err
	}

	p, err := s.d.Projects.RemoveMember(ctx, key, req.GetName(), req.GetGroup())
	if err != nil {
		return nil, err
	}

	out, err := s.projectWithRepositories(ctx, p)
	if err != nil {
		return nil, err
	}

	return &relicv1.RemoveProjectMemberResponse{
		Project: out,
	}, nil
}

func (s *projectServer) projectWithRepositories(ctx context.Context, p project.Project) (*relicv1.Project, error) {
	repos, err := s.d.Repositories.Projects(ctx)
	if err != nil {
		return nil, err
	}

	return s.projectMessage(ctx, p, repos)
}

// administersProject fails unless the caller is an administrator or an admin
// of the project key.
func administersProject(ctx context.Context, d Dependencies, key string) error {
	ok, err := d.Projects.Administers(ctx, auth.FromContext(ctx), key)
	if err != nil {
		return err
	}

	if !ok {
		return connect.NewError(
			connect.CodePermissionDenied,
			"only administrators and the project's admins may do this",
		)
	}

	return nil
}
