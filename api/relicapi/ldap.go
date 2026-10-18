package relicapi

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect/v2"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/access/ldap"
)

type ldapServer struct {
	d Dependencies
}

func directoryMessage(st ldap.Server) *relicv1.LdapServer {
	return &relicv1.LdapServer{
		Name:          st.Name,
		Enabled:       st.Enabled,
		Url:           st.URL,
		UserDnPattern: st.UserDNPattern,
		Search: &relicv1.LdapUserSearch{
			Filter:    st.Search.Filter,
			Base:      st.Search.Base,
			SubTree:   st.Search.SubTree,
			ManagerDn: st.Search.ManagerDN,
		},
		AutoCreateUser:     st.AutoCreateUser,
		EmailAttribute:     st.EmailAttribute,
		StartTls:           st.StartTLS,
		HasManagerPassword: st.Search.ManagerPassword != "",
	}
}

func directoryOf(m *relicv1.LdapServer) ldap.Server {
	s := m.GetSearch()

	return ldap.Server{
		Name:          m.GetName(),
		Enabled:       m.GetEnabled(),
		URL:           m.GetUrl(),
		UserDNPattern: m.GetUserDnPattern(),
		Search: ldap.Search{
			Filter:          s.GetFilter(),
			Base:            s.GetBase(),
			SubTree:         s.GetSubTree(),
			ManagerDN:       s.GetManagerDn(),
			ManagerPassword: s.GetManagerPassword(),
		},
		AutoCreateUser: m.GetAutoCreateUser(),
		EmailAttribute: m.GetEmailAttribute(),
		StartTLS:       m.GetStartTls(),
	}
}

func mappingMessage(g ldap.GroupMapping) *relicv1.LdapGroupMapping {
	return &relicv1.LdapGroupMapping{
		Name:                 g.Name,
		LdapServer:           g.Server,
		BaseDn:               g.BaseDN,
		NameAttribute:        g.NameAttribute,
		MemberAttribute:      g.MemberAttribute,
		SubTree:              g.SubTree,
		Filter:               g.Filter,
		DescriptionAttribute: g.DescriptionAttribute,
		Strategy:             fromDomain[relicv1.GroupStrategy](g.Strategy),
	}
}

func (s *ldapServer) ListLdapServers(
	ctx context.Context,
	_ *relicv1.ListLdapServersRequest,
) (*relicv1.ListLdapServersResponse, error) {
	all, err := s.d.LDAP.Servers(ctx)
	if err != nil {
		return nil, err
	}

	out := &relicv1.ListLdapServersResponse{
		LdapServers: make([]*relicv1.LdapServer, 0, len(all)),
	}
	for _, st := range all {
		out.LdapServers = append(out.LdapServers, directoryMessage(st))
	}

	return out, nil
}

func (s *ldapServer) PutLdapServer(
	ctx context.Context,
	req *relicv1.PutLdapServerRequest,
) (*relicv1.PutLdapServerResponse, error) {
	st := directoryOf(req.GetLdapServer())
	if old, err := s.d.LDAP.Server(ctx, st.Name); err == nil {
		st.Extensions = old.Extensions
	} else if !errors.Is(err, ldap.ErrNotFound) {
		return nil, err
	}

	if err := s.d.LDAP.PutLdapServer(ctx, st); err != nil {
		return nil, err
	}

	return &relicv1.PutLdapServerResponse{}, nil
}

// DeleteLdapServer refuses to delete a server that group mappings read.
func (s *ldapServer) DeleteLdapServer(
	ctx context.Context,
	req *relicv1.DeleteLdapServerRequest,
) (*relicv1.DeleteLdapServerResponse, error) {
	mappings, err := s.d.LDAP.GroupMappings(ctx)
	if err != nil {
		return nil, err
	}

	var users []string

	for _, m := range mappings {
		if m.Server == req.GetName() {
			users = append(users, m.Name)
		}
	}

	if len(users) > 0 {
		return nil, connect.Errorf(
			connect.CodeFailedPrecondition,

			"LDAP server %s is used by the group mappings %s; delete them or point them at another server first",
			req.GetName(),
			strings.Join(users, ", "),
		)
	}

	if err := s.d.LDAP.DeleteLdapServer(ctx, req.GetName()); err != nil {
		return nil, err
	}

	return &relicv1.DeleteLdapServerResponse{}, nil
}

func (s *ldapServer) ListLdapGroupMappings(
	ctx context.Context,
	_ *relicv1.ListLdapGroupMappingsRequest,
) (*relicv1.ListLdapGroupMappingsResponse, error) {
	all, err := s.d.LDAP.GroupMappings(ctx)
	if err != nil {
		return nil, err
	}

	out := &relicv1.ListLdapGroupMappingsResponse{
		LdapGroupMappings: make([]*relicv1.LdapGroupMapping, 0, len(all)),
	}
	for _, g := range all {
		out.LdapGroupMappings = append(out.LdapGroupMappings, mappingMessage(g))
	}

	return out, nil
}

func (s *ldapServer) PutLdapGroupMapping(
	ctx context.Context,
	req *relicv1.PutLdapGroupMappingRequest,
) (*relicv1.PutLdapGroupMappingResponse, error) {
	m := req.GetLdapGroupMapping()
	strategy, _ := toDomain[ldap.GroupStrategy](m.GetStrategy())

	g := ldap.GroupMapping{
		Name:                 m.GetName(),
		Server:               m.GetLdapServer(),
		BaseDN:               m.GetBaseDn(),
		NameAttribute:        m.GetNameAttribute(),
		MemberAttribute:      m.GetMemberAttribute(),
		SubTree:              m.GetSubTree(),
		Filter:               m.GetFilter(),
		DescriptionAttribute: m.GetDescriptionAttribute(),
		Strategy:             strategy,
	}
	old, err := s.d.LDAP.GroupMapping(ctx, g.Name)

	switch {
	case err == nil:
		g.Extensions = old.Extensions
	case !errors.Is(err, ldap.ErrNotFound):
		return nil, fmt.Errorf("read the stored group mapping: %w", err)
	}

	if err := s.d.LDAP.PutLdapGroupMapping(ctx, g); err != nil {
		return nil, err
	}

	return &relicv1.PutLdapGroupMappingResponse{}, nil
}

func (s *ldapServer) DeleteLdapGroupMapping(
	ctx context.Context,
	req *relicv1.DeleteLdapGroupMappingRequest,
) (*relicv1.DeleteLdapGroupMappingResponse, error) {
	if err := s.d.LDAP.DeleteLdapGroupMapping(ctx, req.GetName()); err != nil {
		return nil, err
	}

	return &relicv1.DeleteLdapGroupMappingResponse{}, nil
}

func (s *ldapServer) TestLdapSignIn(
	ctx context.Context,
	req *relicv1.TestLdapSignInRequest,
) (*relicv1.TestLdapSignInResponse, error) {
	e, err := s.d.LDAP.Test(ctx, directoryOf(req.GetLdapServer()), req.GetUsername(), req.GetPassword())
	switch {
	case errors.Is(err, ldap.ErrInvalidCredentials):
		return &relicv1.TestLdapSignInResponse{
			Error: "The directory did not accept these credentials.",
		}, nil
	case errors.Is(err, ldap.ErrUnavailable):
		return &relicv1.TestLdapSignInResponse{
			Error: err.Error(),
		}, nil
	case err != nil:
		return nil, err
	}

	return &relicv1.TestLdapSignInResponse{
		Ok:     true,
		Dn:     e.DN,
		Email:  e.Email,
		Groups: e.Groups,
	}, nil
}
