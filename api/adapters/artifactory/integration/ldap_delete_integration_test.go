//go:build integration

package integration

import (
	"testing"

	"connectrpc.com/connect/v2"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
)

func TestRelicAPIKeepsLdapServersInUse(t *testing.T) {
	t.Parallel()
	srv := server(t)
	ctx := t.Context()
	lc := relicv1connect.NewLdapServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))

	put := &relicv1.LdapServer{
		Name:    "corp",
		Enabled: true,
		Url:     "ldap://127.0.0.1:1/dc=example,dc=com",
		Search: &relicv1.LdapUserSearch{
			Filter: "uid={0}",
		},
	}
	if _, err := lc.PutLdapServer(ctx, &relicv1.PutLdapServerRequest{
		LdapServer: put,
	}); err != nil {
		t.Fatal(err)
	}

	mapping := &relicv1.LdapGroupMapping{
		Name:            "corp-groups",
		LdapServer:      "corp",
		Strategy:        relicv1.GroupStrategy_GROUP_STRATEGY_STATIC,
		BaseDn:          "ou=groups",
		NameAttribute:   "cn",
		MemberAttribute: "uniqueMember",
	}
	if _, err := lc.PutLdapGroupMapping(
		ctx,
		&relicv1.PutLdapGroupMappingRequest{
			LdapGroupMapping: mapping,
		},
	); err != nil {
		t.Fatal(err)
	}

	_, err := lc.DeleteLdapServer(ctx, &relicv1.DeleteLdapServerRequest{
		Name: "corp",
	})
	if code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("delete in use = %v, want failed_precondition", err)
	}

	if _, err := lc.DeleteLdapGroupMapping(
		ctx,
		&relicv1.DeleteLdapGroupMappingRequest{
			Name: "corp-groups",
		},
	); err != nil {
		t.Fatal(err)
	}

	if _, err := lc.DeleteLdapServer(
		ctx,
		&relicv1.DeleteLdapServerRequest{
			Name: "corp",
		},
	); err != nil {
		t.Errorf("delete unused = %v", err)
	}
}
