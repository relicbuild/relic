package relicapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/access/auth"
	"relic/internal/access/project"
	"relic/internal/access/security"
)

type securityServer struct {
	d Dependencies
}

func userMessage(u security.User) *relicv1.User {
	return &relicv1.User{
		Name:     u.Name,
		Email:    u.Email,
		Admin:    u.Admin,
		Viewer:   u.Viewer,
		Disabled: u.Disabled,
		Groups:   u.Groups,
		Realm:    fromDomain[relicv1.Realm](u.Realm),
	}
}

func (s *securityServer) ListUsers(
	ctx context.Context,
	_ *connect.Request[relicv1.ListUsersRequest],
) (*connect.Response[relicv1.ListUsersResponse], error) {
	names, err := s.d.Security.ListUsers(ctx)
	if err != nil {
		return nil, err
	}

	out := &relicv1.ListUsersResponse{
		Users: make([]*relicv1.User, 0, len(names)),
	}
	for _, n := range names {
		u, err := s.d.Security.GetUser(ctx, n)
		if errors.Is(err, security.ErrNotFound) {
			continue
		}

		if err != nil {
			return nil, err
		}

		out.Users = append(out.Users, userMessage(u))
	}

	return connect.NewResponse(out), nil
}

func (s *securityServer) GetUser(
	ctx context.Context,
	req *connect.Request[relicv1.GetUserRequest],
) (*connect.Response[relicv1.GetUserResponse], error) {
	u, err := s.d.Security.GetUser(ctx, req.Msg.GetName())
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.GetUserResponse{
		User: userMessage(u),
	}), nil
}

func (s *securityServer) PutUser(
	ctx context.Context,
	req *connect.Request[relicv1.PutUserRequest],
) (*connect.Response[relicv1.PutUserResponse], error) {
	m := req.Msg.GetUser()

	u := security.User{
		Name:     m.GetName(),
		Email:    m.GetEmail(),
		Admin:    m.GetAdmin(),
		Viewer:   m.GetViewer(),
		Groups:   m.GetGroups(),
		Password: m.GetPassword(),
		Disabled: m.GetDisabled(),
	}
	old, err := s.d.Security.GetUser(ctx, u.Name)

	switch {
	case err == nil:
		u.Extensions = old.Extensions
	case !errors.Is(err, security.ErrNotFound):
		return nil, fmt.Errorf("read the stored user: %w", err)
	}

	if m.GetDisabled() && strings.EqualFold(u.Name, auth.FromContext(ctx).Username) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("you cannot disable your own account"))
	}

	created, err := s.d.Security.ReplaceUser(ctx, u)
	if err != nil {
		return nil, err
	}

	saved, err := s.d.Security.GetUser(ctx, u.Name)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.PutUserResponse{
		User:    userMessage(saved),
		Created: created,
	}), nil
}

func (s *securityServer) DeleteUser(
	ctx context.Context,
	req *connect.Request[relicv1.DeleteUserRequest],
) (*connect.Response[relicv1.DeleteUserResponse], error) {
	if err := s.d.Security.DeleteUser(ctx, req.Msg.GetName()); err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.DeleteUserResponse{}), nil
}

func groupMessage(g security.Group) *relicv1.Group {
	return &relicv1.Group{
		Name:               g.Name,
		Description:        g.Description,
		DefaultForNewUsers: g.DefaultForNewUsers,
		Admin:              g.Admin,
		Viewer:             g.Viewer,
		Members:            g.Members,
	}
}

func (s *securityServer) ListGroups(
	ctx context.Context,
	_ *connect.Request[relicv1.ListGroupsRequest],
) (*connect.Response[relicv1.ListGroupsResponse], error) {
	names, err := s.d.Security.ListGroups(ctx)
	if err != nil {
		return nil, err
	}

	out := &relicv1.ListGroupsResponse{
		Groups: make([]*relicv1.Group, 0, len(names)),
	}
	for _, n := range names {
		g, err := s.d.Security.GetGroup(ctx, n, true)
		if errors.Is(err, security.ErrNotFound) {
			continue
		}

		if err != nil {
			return nil, err
		}

		out.Groups = append(out.Groups, groupMessage(g))
	}

	return connect.NewResponse(out), nil
}

func (s *securityServer) GetGroup(
	ctx context.Context,
	req *connect.Request[relicv1.GetGroupRequest],
) (*connect.Response[relicv1.GetGroupResponse], error) {
	g, err := s.d.Security.GetGroup(ctx, req.Msg.GetName(), true)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.GetGroupResponse{
		Group: groupMessage(g),
	}), nil
}

func (s *securityServer) PutGroup(
	ctx context.Context,
	req *connect.Request[relicv1.PutGroupRequest],
) (*connect.Response[relicv1.PutGroupResponse], error) {
	m := req.Msg.GetGroup()

	members := m.GetMembers()
	if members == nil {
		members = []string{}
	}

	created, err := s.d.Security.PutGroup(ctx, security.Group{
		Name:               m.GetName(),
		Description:        m.GetDescription(),
		DefaultForNewUsers: m.GetDefaultForNewUsers(),
		Admin:              m.GetAdmin(),
		Viewer:             m.GetViewer(),
	}, members)
	if err != nil {
		return nil, err
	}

	g, err := s.d.Security.GetGroup(ctx, m.GetName(), true)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.PutGroupResponse{
		Group:   groupMessage(g),
		Created: created,
	}), nil
}

func (s *securityServer) DeleteGroup(
	ctx context.Context,
	req *connect.Request[relicv1.DeleteGroupRequest],
) (*connect.Response[relicv1.DeleteGroupResponse], error) {
	if err := s.d.Security.DeleteGroup(ctx, req.Msg.GetName()); err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.DeleteGroupResponse{}), nil
}

func actionMessages(actions []auth.Action) []relicv1.Action {
	out := make([]relicv1.Action, 0, len(actions))
	for _, a := range actions {
		out = append(out, fromDomain[relicv1.Action](a))
	}

	return out
}

// PermissionMessage renders p.
func PermissionMessage(p security.Permission) *relicv1.Permission {
	out := &relicv1.Permission{
		Name:  p.Name,
		Rules: make([]*relicv1.PermissionRule, 0, len(p.Rules)),
	}
	for _, r := range p.Rules {
		m := &relicv1.PermissionRule{
			Resource: fromDomain[relicv1.ResourceKind](r.Resource),
			Include:  r.Include,
			Exclude:  r.Exclude,
		}
		if len(r.Selector.Repositories) > 0 || len(r.Selector.Sets) > 0 {
			m.Selector = &relicv1.RepositorySelector{
				Repositories: r.Selector.Repositories,
			}
			for _, set := range r.Selector.Sets {
				m.Selector.Sets = append(m.Selector.Sets, fromDomain[relicv1.RepositorySet](set))
			}
		}

		for _, g := range r.Grants {
			gm := &relicv1.PermissionGrant{
				Actions: actionMessages(g.Actions),
			}
			if g.User != "" {
				gm.Principal = &relicv1.PermissionGrant_User{
					User: g.User,
				}
			} else {
				gm.Principal = &relicv1.PermissionGrant_Group{
					Group: g.Group,
				}
			}

			m.Grants = append(m.Grants, gm)
		}

		out.Rules = append(out.Rules, m)
	}

	return out
}

func permissionOf(m *relicv1.Permission) (security.Permission, error) {
	p := security.Permission{
		Name:  m.GetName(),
		Rules: []security.Rule{},
	}
	for _, rm := range m.GetRules() {
		kind, ok := toDomain[security.ResourceKind](rm.GetResource())
		if !ok {
			return security.Permission{}, fmt.Errorf("%w: a rule needs a resource", security.ErrInvalid)
		}

		r := security.Rule{
			Resource: kind,
			Include:  rm.GetInclude(),
			Exclude:  rm.GetExclude(),
			Grants:   []security.Grant{},
			Selector: auth.Selector{
				Repositories: rm.GetSelector().GetRepositories(),
			},
		}
		for _, sm := range rm.GetSelector().GetSets() {
			set, ok := toDomain[auth.RepositorySet](sm)
			if !ok {
				return security.Permission{}, fmt.Errorf("%w: unknown repository set %d", security.ErrInvalid, sm)
			}

			r.Selector.Sets = append(r.Selector.Sets, set)
		}

		for _, gm := range rm.GetGrants() {
			g := security.Grant{
				User:    gm.GetUser(),
				Group:   gm.GetGroup(),
				Actions: []auth.Action{},
			}
			for _, am := range gm.GetActions() {
				a, ok := toDomain[auth.Action](am)
				if !ok {
					return security.Permission{}, fmt.Errorf("%w: unknown action %d", security.ErrInvalid, am)
				}

				g.Actions = append(g.Actions, a)
			}

			r.Grants = append(r.Grants, g)
		}

		p.Rules = append(p.Rules, r)
	}

	return p, nil
}

func (s *securityServer) ListPermissions(
	ctx context.Context,
	_ *connect.Request[relicv1.ListPermissionsRequest],
) (*connect.Response[relicv1.ListPermissionsResponse], error) {
	names, err := s.d.Security.ListPermissions(ctx)
	if err != nil {
		return nil, err
	}

	out := &relicv1.ListPermissionsResponse{
		Permissions: make([]*relicv1.Permission, 0, len(names)),
	}
	for _, n := range names {
		p, err := s.d.Security.GetPermission(ctx, n)
		if errors.Is(err, security.ErrNotFound) {
			continue
		}

		if err != nil {
			return nil, err
		}

		out.Permissions = append(out.Permissions, PermissionMessage(p))
	}

	return connect.NewResponse(out), nil
}

func (s *securityServer) GetPermission(
	ctx context.Context,
	req *connect.Request[relicv1.GetPermissionRequest],
) (*connect.Response[relicv1.GetPermissionResponse], error) {
	p, err := s.d.Security.GetPermission(ctx, req.Msg.GetName())
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.GetPermissionResponse{
		Permission: PermissionMessage(p),
	}), nil
}

func (s *securityServer) PutPermission(
	ctx context.Context,
	req *connect.Request[relicv1.PutPermissionRequest],
) (*connect.Response[relicv1.PutPermissionResponse], error) {
	p, err := permissionOf(req.Msg.GetPermission())
	if err != nil {
		return nil, err
	}

	err = s.d.Security.UpdatePermission(ctx, p)
	if errors.Is(err, security.ErrNotFound) {
		err = s.d.Security.CreatePermission(ctx, p)
	}

	if errors.Is(err, security.ErrExists) {
		err = s.d.Security.UpdatePermission(ctx, p)
	}

	if err != nil {
		return nil, err
	}

	saved, err := s.d.Security.GetPermission(ctx, p.Name)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.PutPermissionResponse{
		Permission: PermissionMessage(saved),
	}), nil
}

func (s *securityServer) DeletePermission(
	ctx context.Context,
	req *connect.Request[relicv1.DeletePermissionRequest],
) (*connect.Response[relicv1.DeletePermissionResponse], error) {
	if err := s.d.Security.DeletePermission(ctx, req.Msg.GetName()); err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.DeletePermissionResponse{}), nil
}

const maxTokenDays = 3650

func (s *securityServer) CreateToken(
	ctx context.Context,
	req *connect.Request[relicv1.CreateTokenRequest],
) (*connect.Response[relicv1.CreateTokenResponse], error) {
	if s.d.Tokens == nil {
		return nil, unimplemented("tokens")
	}

	days := req.Msg.GetExpiresDays()
	if days < 0 || days > maxTokenDays {
		return nil, connect.NewError(
			connect.CodeInvalidArgument,
			fmt.Errorf("expires_days must be 0 to %d", maxTokenDays),
		)
	}

	caller := auth.FromContext(ctx)
	scope := auth.UserScope

	if sm := req.Msg.GetScope(); sm != nil {
		kind, ok := toDomain[auth.ScopeKind](sm.GetKind())

		switch {
		case !ok && sm.GetKind() != relicv1.TokenScopeKind_TOKEN_SCOPE_KIND_UNSPECIFIED:
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown scope kind %d", sm.GetKind()))
		case !ok:
			kind = auth.ScopeUser
		}

		scope = auth.TokenScope{
			Kind:   kind,
			Groups: sm.GetGroups(),
		}
	}

	if !caller.MayGrant(scope) {
		return nil, connect.NewError(
			connect.CodePermissionDenied,
			errors.New("only administrators can create admin tokens or tokens for groups they are not in"),
		)
	}

	if key := req.Msg.GetProject(); key != "" {
		if err := s.requireVisibleProject(ctx, caller, key); err != nil {
			return nil, err
		}
	}

	tok, err := s.d.Tokens.Issue(ctx, caller.LimitIssue(auth.IssueParams{
		Username:    caller.Name(),
		Scope:       scope,
		ExpiresIn:   time.Duration(days) * 24 * time.Hour,
		Description: req.Msg.GetDescription(),
		Project:     req.Msg.GetProject(),
	}, time.Now()))
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.CreateTokenResponse{
		AccessToken: tok.AccessToken,
		ExpiresIn:   int64(tok.ExpiresIn / time.Second),
		Scope: &relicv1.TokenScope{
			Kind:   fromDomain[relicv1.TokenScopeKind](tok.Scope.Kind),
			Groups: tok.Scope.Groups,
		},
	}), nil
}

func (s *securityServer) requireVisibleProject(ctx context.Context, caller auth.Principal, key string) error {
	if s.d.Projects == nil {
		return unimplemented("projects")
	}

	p, err := s.d.Projects.Get(ctx, key)
	if err != nil {
		return err
	}

	if !seesAllProjects(caller) && p.Role(caller) == "" {
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("%w: %s", project.ErrNotFound, key))
	}

	return nil
}

// ListTokens lists the caller's own tokens, or for an administrator or a viewer
// everyone's (or one user's).
func (s *securityServer) ListTokens(
	ctx context.Context,
	req *connect.Request[relicv1.ListTokensRequest],
) (*connect.Response[relicv1.ListTokensResponse], error) {
	caller := auth.FromContext(ctx)

	user := req.Msg.GetUsername()
	if !caller.Admin && !caller.Viewer {
		user = caller.Name()
	}

	list, err := s.d.Tokens.List(ctx, user, req.Msg.GetIncludeRevoked())
	if err != nil {
		return nil, err
	}

	out := &relicv1.ListTokensResponse{
		Tokens: make([]*relicv1.AccessToken, 0, len(list)),
	}
	for _, t := range list {
		m := &relicv1.AccessToken{
			Id:          t.ID,
			Username:    t.Username,
			Description: t.Description,
			Project:     t.Project,
			Scope: &relicv1.TokenScope{
				Kind:   fromDomain[relicv1.TokenScopeKind](t.Scope.Kind),
				Groups: t.Scope.Groups,
			},
			Created:     timestamppb.New(t.Created),
			Revoked:     t.Revoked,
			Refreshable: t.Refreshable,
		}
		if t.Expires != nil {
			m.Expires = timestamppb.New(*t.Expires)
		}

		out.Tokens = append(out.Tokens, m)
	}

	return connect.NewResponse(out), nil
}

// RevokeToken revokes one of the caller's tokens, or any token for an
// administrator.
func (s *securityServer) RevokeToken(
	ctx context.Context,
	req *connect.Request[relicv1.RevokeTokenRequest],
) (*connect.Response[relicv1.RevokeTokenResponse], error) {
	caller := auth.FromContext(ctx)

	owner := caller.Name()
	if caller.Admin {
		owner = ""
	}

	if err := s.d.Tokens.RevokeID(ctx, req.Msg.GetId(), owner); err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.RevokeTokenResponse{}), nil
}
