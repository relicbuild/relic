package main

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	relicv1 "relic/api/gen/relic/v1"
)

type kind struct {
	name      string
	aliases   []string
	nameField string
	new       func() proto.Message
	list      func(context.Context, *client) ([]proto.Message, error)
	// get answers a connect.CodeNotFound error for a missing resource.
	get     func(context.Context, *client, string) (proto.Message, error)
	create  func(context.Context, *client, proto.Message) error
	update  func(context.Context, *client, proto.Message) error
	delete  func(context.Context, *client, string) error
	ignored []string
	prepare func(map[string]any)
}

const apiVersion = "relic.build/v1"

var kinds = []*kind{
	{
		name: "Repository",
		aliases: []string{
			"repository",
			"repositories",
			"repo",
			"repos",
		},
		nameField: "name",
		new: func() proto.Message {
			return &relicv1.Repository{}
		},
		list: func(ctx context.Context, c *client) ([]proto.Message, error) {
			r, err := c.repositories.ListRepositories(ctx, connect.NewRequest(&relicv1.ListRepositoriesRequest{}))
			return messages(r, err, (*relicv1.ListRepositoriesResponse).GetRepositories)
		},
		get: func(ctx context.Context, c *client, name string) (proto.Message, error) {
			r, err := c.repositories.GetRepository(ctx, connect.NewRequest(&relicv1.GetRepositoryRequest{
				Name: name,
			}))

			return message(r, err, (*relicv1.GetRepositoryResponse).GetRepository)
		},
		create: func(ctx context.Context, c *client, m proto.Message) error {
			_, err := c.repositories.CreateRepository(
				ctx,
				connect.NewRequest(&relicv1.CreateRepositoryRequest{
					Repository: m.(*relicv1.Repository),
				}),
			)

			return err
		},
		update: func(ctx context.Context, c *client, m proto.Message) error {
			_, err := c.repositories.UpdateRepository(
				ctx,
				connect.NewRequest(&relicv1.UpdateRepositoryRequest{
					Repository: m.(*relicv1.Repository),
				}),
			)

			return err
		},
		delete: func(ctx context.Context, c *client, name string) error {
			_, err := c.repositories.DeleteRepository(
				ctx,
				connect.NewRequest(&relicv1.DeleteRepositoryRequest{
					Name: name,
				}),
			)

			return err
		},
		ignored: []string{
			"remote.password",
			"created",
			"updated",
			"usedBytes",
		},
	},
	{
		name: "User",
		aliases: []string{
			"user",
			"users",
		},
		nameField: "name",
		new: func() proto.Message {
			return &relicv1.User{}
		},
		list: func(ctx context.Context, c *client) ([]proto.Message, error) {
			r, err := c.security.ListUsers(ctx, connect.NewRequest(&relicv1.ListUsersRequest{}))
			return messages(r, err, (*relicv1.ListUsersResponse).GetUsers)
		},
		get: func(ctx context.Context, c *client, name string) (proto.Message, error) {
			r, err := c.security.GetUser(ctx, connect.NewRequest(&relicv1.GetUserRequest{
				Name: name,
			}))

			return message(r, err, (*relicv1.GetUserResponse).GetUser)
		},
		create: putUser,
		update: putUser,
		delete: func(ctx context.Context, c *client, name string) error {
			_, err := c.security.DeleteUser(ctx, connect.NewRequest(&relicv1.DeleteUserRequest{
				Name: name,
			}))

			return err
		},
		ignored: []string{
			"password",
		},
	},
	{
		name: "Group",
		aliases: []string{
			"group",
			"groups",
		},
		nameField: "name",
		new: func() proto.Message {
			return &relicv1.Group{}
		},
		list: func(ctx context.Context, c *client) ([]proto.Message, error) {
			r, err := c.security.ListGroups(ctx, connect.NewRequest(&relicv1.ListGroupsRequest{}))
			return messages(r, err, (*relicv1.ListGroupsResponse).GetGroups)
		},
		get: func(ctx context.Context, c *client, name string) (proto.Message, error) {
			r, err := c.security.GetGroup(ctx, connect.NewRequest(&relicv1.GetGroupRequest{
				Name: name,
			}))

			return message(r, err, (*relicv1.GetGroupResponse).GetGroup)
		},
		create: putGroup,
		update: putGroup,
		delete: func(ctx context.Context, c *client, name string) error {
			_, err := c.security.DeleteGroup(ctx, connect.NewRequest(&relicv1.DeleteGroupRequest{
				Name: name,
			}))

			return err
		},
	},
	{
		name: "Permission",
		aliases: []string{
			"permission",
			"permissions",
			"perm",
			"perms",
		},
		nameField: "name",
		new: func() proto.Message {
			return &relicv1.Permission{}
		},
		list: func(ctx context.Context, c *client) ([]proto.Message, error) {
			r, err := c.security.ListPermissions(ctx, connect.NewRequest(&relicv1.ListPermissionsRequest{}))
			return messages(r, err, (*relicv1.ListPermissionsResponse).GetPermissions)
		},
		get: func(ctx context.Context, c *client, name string) (proto.Message, error) {
			r, err := c.security.GetPermission(ctx, connect.NewRequest(&relicv1.GetPermissionRequest{
				Name: name,
			}))

			return message(r, err, (*relicv1.GetPermissionResponse).GetPermission)
		},
		create: putPermission,
		update: putPermission,
		delete: func(ctx context.Context, c *client, name string) error {
			_, err := c.security.DeletePermission(ctx, connect.NewRequest(&relicv1.DeletePermissionRequest{
				Name: name,
			}))

			return err
		},
	},
	{
		name: "LdapServer",
		aliases: []string{
			"ldapserver",
			"ldapservers",
			"ldap",
		},
		nameField: "name",
		new: func() proto.Message {
			return &relicv1.LdapServer{}
		},
		list: listDirectories,
		get: func(ctx context.Context, c *client, name string) (proto.Message, error) {
			return findIn(ctx, c, listDirectories, "name", name)
		},
		create: putLdapServer,
		update: putLdapServer,
		delete: func(ctx context.Context, c *client, name string) error {
			_, err := c.ldap.DeleteLdapServer(ctx, connect.NewRequest(&relicv1.DeleteLdapServerRequest{
				Name: name,
			}))

			return err
		},
		ignored: []string{
			"search.managerPassword",
			"hasManagerPassword",
		},
	},
	{
		name: "IdentityProvider",
		aliases: []string{
			"identityprovider",
			"identityproviders",
			"idp",
			"sso",
		},
		nameField: "name",
		new: func() proto.Message {
			return &relicv1.IdentityProvider{}
		},
		list: listIdentityProviders,
		get: func(ctx context.Context, c *client, name string) (proto.Message, error) {
			r, err := c.identityProviders.GetIdentityProvider(
				ctx,
				connect.NewRequest(&relicv1.GetIdentityProviderRequest{
					Name: name,
				}),
			)
			if err != nil {
				return nil, err
			}

			return r.Msg.GetIdentityProvider(), nil
		},
		create: putIdentityProvider,
		update: putIdentityProvider,
		delete: func(ctx context.Context, c *client, name string) error {
			_, err := c.identityProviders.DeleteIdentityProvider(
				ctx,
				connect.NewRequest(&relicv1.DeleteIdentityProviderRequest{
					Name: name,
				}),
			)

			return err
		},
		ignored: []string{
			"oidc.clientSecret",
			"saml.certificate",
			"callbackPath",
		},
	},
	{
		name: "PromotionGate",
		aliases: []string{
			"promotiongate",
			"promotiongates",
			"gate",
			"gates",
		},
		nameField: "name",
		new: func() proto.Message {
			return &relicv1.PromotionGate{}
		},
		list: func(ctx context.Context, c *client) ([]proto.Message, error) {
			r, err := c.promotionGates.ListPromotionGates(
				ctx,
				connect.NewRequest(&relicv1.ListPromotionGatesRequest{}),
			)

			return messages(r, err, (*relicv1.ListPromotionGatesResponse).GetPromotionGates)
		},
		get: func(ctx context.Context, c *client, name string) (proto.Message, error) {
			r, err := c.promotionGates.GetPromotionGate(
				ctx,
				connect.NewRequest(&relicv1.GetPromotionGateRequest{
					Name: name,
				}),
			)
			if err != nil {
				return nil, err
			}

			return r.Msg.GetPromotionGate(), nil
		},
		create: putPromotionGate,
		update: putPromotionGate,
		delete: func(ctx context.Context, c *client, name string) error {
			_, err := c.promotionGates.DeletePromotionGate(
				ctx,
				connect.NewRequest(&relicv1.DeletePromotionGateRequest{
					Name: name,
				}),
			)

			return err
		},
		ignored: []string{
			"created",
			"updated",
		},
	},
	{
		name: "Webhook",
		aliases: []string{
			"webhook",
			"webhooks",
			"hook",
			"hooks",
		},
		nameField: "name",
		new: func() proto.Message {
			return &relicv1.Webhook{}
		},
		list: func(ctx context.Context, c *client) ([]proto.Message, error) {
			r, err := c.webhooks.ListWebhooks(
				ctx,
				connect.NewRequest(&relicv1.ListWebhooksRequest{}),
			)

			return messages(r, err, (*relicv1.ListWebhooksResponse).GetWebhooks)
		},
		get: func(ctx context.Context, c *client, name string) (proto.Message, error) {
			r, err := c.webhooks.GetWebhook(
				ctx,
				connect.NewRequest(&relicv1.GetWebhookRequest{
					Name: name,
				}),
			)
			if err != nil {
				return nil, err
			}

			return r.Msg.GetWebhook(), nil
		},
		create: putWebhook,
		update: putWebhook,
		delete: func(ctx context.Context, c *client, name string) error {
			_, err := c.webhooks.DeleteWebhook(
				ctx,
				connect.NewRequest(&relicv1.DeleteWebhookRequest{
					Name: name,
				}),
			)

			return err
		},
		ignored: []string{
			"secret",
			"hasSecret",
			"created",
			"updated",
		},
	},
	{
		name: "Project",
		aliases: []string{
			"project",
			"projects",
			"proj",
		},
		nameField: "key",
		new: func() proto.Message {
			return &relicv1.Project{}
		},
		list: func(ctx context.Context, c *client) ([]proto.Message, error) {
			r, err := c.projects.ListProjects(
				ctx,
				connect.NewRequest(&relicv1.ListProjectsRequest{}),
			)

			return messages(r, err, (*relicv1.ListProjectsResponse).GetProjects)
		},
		get: func(ctx context.Context, c *client, name string) (proto.Message, error) {
			r, err := c.projects.GetProject(
				ctx,
				connect.NewRequest(&relicv1.GetProjectRequest{
					Key: name,
				}),
			)
			if err != nil {
				return nil, err
			}

			return r.Msg.GetProject(), nil
		},
		create: putProject,
		update: putProject,
		delete: func(ctx context.Context, c *client, name string) error {
			_, err := c.projects.DeleteProject(
				ctx,
				connect.NewRequest(&relicv1.DeleteProjectRequest{
					Key: name,
				}),
			)

			return err
		},
		ignored: []string{
			"usedBytes",
			"repositories",
			"created",
			"updated",
		},
	},
	{
		name: "WorkloadIdentity",
		aliases: []string{
			"workloadidentity",
			"workloadidentities",
			"workload",
		},
		nameField: "name",
		new: func() proto.Message {
			return &relicv1.WorkloadIdentity{}
		},
		list: listWorkloadIdentities,
		get: func(ctx context.Context, c *client, name string) (proto.Message, error) {
			r, err := c.workloadIdentities.GetWorkloadIdentity(
				ctx,
				connect.NewRequest(&relicv1.GetWorkloadIdentityRequest{
					Name: name,
				}),
			)
			if err != nil {
				return nil, err
			}

			return r.Msg.GetWorkloadIdentity(), nil
		},
		create: putWorkloadIdentity,
		update: putWorkloadIdentity,
		delete: func(ctx context.Context, c *client, name string) error {
			_, err := c.workloadIdentities.DeleteWorkloadIdentity(
				ctx,
				connect.NewRequest(&relicv1.DeleteWorkloadIdentityRequest{
					Name: name,
				}),
			)

			return err
		},
	},
	{
		name: "LdapGroupMapping",
		aliases: []string{
			"ldapgroupmapping",
			"ldapgroupmappings",
			"groupmapping",
			"groupmappings",
			"ldapgroup",
			"ldapgroups",
		},
		nameField: "name",
		new: func() proto.Message {
			return &relicv1.LdapGroupMapping{}
		},
		list: listMappings,
		get: func(ctx context.Context, c *client, name string) (proto.Message, error) {
			return findIn(ctx, c, listMappings, "name", name)
		},
		create: putMapping,
		update: putMapping,
		delete: func(ctx context.Context, c *client, name string) error {
			_, err := c.ldap.DeleteLdapGroupMapping(
				ctx,
				connect.NewRequest(&relicv1.DeleteLdapGroupMappingRequest{
					Name: name,
				}),
			)

			return err
		},
	},
	{
		name: "Policy",
		aliases: []string{
			"policy",
			"policies",
		},
		nameField: "name",
		new: func() proto.Message {
			return &relicv1.Policy{}
		},
		list: listPolicies,
		get: func(ctx context.Context, c *client, name string) (proto.Message, error) {
			return findIn(ctx, c, listPolicies, "name", name)
		},
		create: putPolicy,
		update: putPolicy,
		delete: func(ctx context.Context, c *client, name string) error {
			_, err := c.scans.DeletePolicy(ctx, connect.NewRequest(&relicv1.DeletePolicyRequest{
				Name: name,
			}))

			return err
		},
		prepare: closeCVSSRanges,
	},
	{
		name: "Watch",
		aliases: []string{
			"watch",
			"watches",
		},
		nameField: "name",
		new: func() proto.Message {
			return &relicv1.Watch{}
		},
		list: listWatches,
		get: func(ctx context.Context, c *client, name string) (proto.Message, error) {
			return findIn(ctx, c, listWatches, "name", name)
		},
		create: putWatch,
		update: putWatch,
		delete: func(ctx context.Context, c *client, name string) error {
			_, err := c.scans.DeleteWatch(ctx, connect.NewRequest(&relicv1.DeleteWatchRequest{
				Name: name,
			}))

			return err
		},
	},
	{
		name: "Replication",
		aliases: []string{
			"replication",
			"replications",
		},
		nameField: "repository",
		new: func() proto.Message {
			return &relicv1.Replication{}
		},
		list: func(ctx context.Context, c *client) ([]proto.Message, error) {
			r, err := c.replications.ListReplications(ctx, connect.NewRequest(&relicv1.ListReplicationsRequest{}))
			return messages(r, err, (*relicv1.ListReplicationsResponse).GetReplications)
		},
		get: func(ctx context.Context, c *client, name string) (proto.Message, error) {
			r, err := c.replications.GetReplication(
				ctx,
				connect.NewRequest(&relicv1.GetReplicationRequest{
					Repository: name,
				}),
			)

			return message(r, err, (*relicv1.GetReplicationResponse).GetReplication)
		},
		create: putReplication,
		update: putReplication,
		delete: func(ctx context.Context, c *client, name string) error {
			_, err := c.replications.DeleteReplication(
				ctx,
				connect.NewRequest(&relicv1.DeleteReplicationRequest{
					Repository: name,
				}),
			)

			return err
		},
		ignored: []string{
			"password",
			"hasPassword",
			"lastRun",
			"lastStatus",
			"lastError",
		},
	},
	{
		name: "CleanupPolicy",
		aliases: []string{
			"cleanuppolicy",
			"cleanuppolicies",
			"cleanup-policy",
			"cleanup-policies",
		},
		nameField: "name",
		new: func() proto.Message {
			return &relicv1.CleanupPolicy{}
		},
		list: func(ctx context.Context, c *client) ([]proto.Message, error) {
			r, err := c.cleanup.ListCleanupPolicies(ctx, connect.NewRequest(&relicv1.ListCleanupPoliciesRequest{}))
			return messages(r, err, (*relicv1.ListCleanupPoliciesResponse).GetPolicies)
		},
		get: func(ctx context.Context, c *client, name string) (proto.Message, error) {
			r, err := c.cleanup.GetCleanupPolicy(
				ctx,
				connect.NewRequest(&relicv1.GetCleanupPolicyRequest{
					Name: name,
				}),
			)

			return message(r, err, (*relicv1.GetCleanupPolicyResponse).GetPolicy)
		},
		create: putCleanupPolicy,
		update: putCleanupPolicy,
		delete: func(ctx context.Context, c *client, name string) error {
			_, err := c.cleanup.DeleteCleanupPolicy(
				ctx,
				connect.NewRequest(&relicv1.DeleteCleanupPolicyRequest{
					Name: name,
				}),
			)

			return err
		},
		ignored: []string{
			"created",
			"updated",
			"lastRun",
			"lastMoved",
			"lastError",
		},
	},
}

func putCleanupPolicy(ctx context.Context, c *client, m proto.Message) error {
	_, err := c.cleanup.PutCleanupPolicy(
		ctx,
		connect.NewRequest(&relicv1.PutCleanupPolicyRequest{
			Policy: m.(*relicv1.CleanupPolicy),
		}),
	)

	return err
}

func putReplication(ctx context.Context, c *client, m proto.Message) error {
	_, err := c.replications.PutReplication(
		ctx,
		connect.NewRequest(&relicv1.PutReplicationRequest{
			Replication: m.(*relicv1.Replication),
		}),
	)

	return err
}

func kindNamed(s string) (*kind, error) {
	for _, k := range kinds {
		if k.name == s || slices.Contains(k.aliases, strings.ToLower(s)) {
			return k, nil
		}
	}

	names := make([]string, 0, len(kinds))
	for _, k := range kinds {
		names = append(names, k.name)
	}

	return nil, fmt.Errorf("unknown kind %q (known: %s)", s, strings.Join(names, ", "))
}

func messages[R any, M proto.Message](r *connect.Response[R], err error, list func(*R) []M) ([]proto.Message, error) {
	if err != nil {
		return nil, err
	}

	out := make([]proto.Message, 0)
	for _, m := range list(r.Msg) {
		out = append(out, m)
	}

	return out, nil
}

func message[R any, M proto.Message](r *connect.Response[R], err error, get func(*R) M) (proto.Message, error) {
	if err != nil {
		return nil, err
	}

	return get(r.Msg), nil
}

func findIn(
	ctx context.Context,
	c *client,
	list func(context.Context, *client) ([]proto.Message, error),
	field, name string,
) (proto.Message, error) {
	all, err := list(ctx, c)
	if err != nil {
		return nil, err
	}

	for _, m := range all {
		if nameOf(m, field) == name {
			return m, nil
		}
	}

	return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("%s not found", name))
}

func putUser(ctx context.Context, c *client, m proto.Message) error {
	_, err := c.security.PutUser(ctx, connect.NewRequest(&relicv1.PutUserRequest{
		User: m.(*relicv1.User),
	}))

	return err
}

func putGroup(ctx context.Context, c *client, m proto.Message) error {
	_, err := c.security.PutGroup(ctx, connect.NewRequest(&relicv1.PutGroupRequest{
		Group: m.(*relicv1.Group),
	}))

	return err
}

func putPermission(ctx context.Context, c *client, m proto.Message) error {
	_, err := c.security.PutPermission(
		ctx,
		connect.NewRequest(&relicv1.PutPermissionRequest{
			Permission: m.(*relicv1.Permission),
		}),
	)

	return err
}

func listDirectories(ctx context.Context, c *client) ([]proto.Message, error) {
	r, err := c.ldap.ListLdapServers(ctx, connect.NewRequest(&relicv1.ListLdapServersRequest{}))
	return messages(r, err, (*relicv1.ListLdapServersResponse).GetLdapServers)
}

func putLdapServer(ctx context.Context, c *client, m proto.Message) error {
	_, err := c.ldap.PutLdapServer(
		ctx,
		connect.NewRequest(&relicv1.PutLdapServerRequest{
			LdapServer: m.(*relicv1.LdapServer),
		}),
	)

	return err
}

func listWorkloadIdentities(ctx context.Context, c *client) ([]proto.Message, error) {
	r, err := c.workloadIdentities.ListWorkloadIdentities(
		ctx,
		connect.NewRequest(&relicv1.ListWorkloadIdentitiesRequest{}),
	)

	return messages(r, err, (*relicv1.ListWorkloadIdentitiesResponse).GetWorkloadIdentities)
}

func putPromotionGate(ctx context.Context, c *client, m proto.Message) error {
	_, err := c.promotionGates.PutPromotionGate(
		ctx,
		connect.NewRequest(&relicv1.PutPromotionGateRequest{
			PromotionGate: m.(*relicv1.PromotionGate),
		}),
	)

	return err
}

func putProject(ctx context.Context, c *client, m proto.Message) error {
	_, err := c.projects.PutProject(
		ctx,
		connect.NewRequest(&relicv1.PutProjectRequest{
			Project: m.(*relicv1.Project),
		}),
	)

	return err
}

func putWebhook(ctx context.Context, c *client, m proto.Message) error {
	_, err := c.webhooks.PutWebhook(
		ctx,
		connect.NewRequest(&relicv1.PutWebhookRequest{
			Webhook: m.(*relicv1.Webhook),
		}),
	)

	return err
}

func putWorkloadIdentity(ctx context.Context, c *client, m proto.Message) error {
	_, err := c.workloadIdentities.PutWorkloadIdentity(
		ctx,
		connect.NewRequest(&relicv1.PutWorkloadIdentityRequest{
			WorkloadIdentity: m.(*relicv1.WorkloadIdentity),
		}),
	)

	return err
}

func listIdentityProviders(ctx context.Context, c *client) ([]proto.Message, error) {
	r, err := c.identityProviders.ListIdentityProviders(
		ctx,
		connect.NewRequest(&relicv1.ListIdentityProvidersRequest{}),
	)

	return messages(r, err, (*relicv1.ListIdentityProvidersResponse).GetIdentityProviders)
}

func putIdentityProvider(ctx context.Context, c *client, m proto.Message) error {
	_, err := c.identityProviders.PutIdentityProvider(
		ctx,
		connect.NewRequest(&relicv1.PutIdentityProviderRequest{
			IdentityProvider: m.(*relicv1.IdentityProvider),
		}),
	)

	return err
}

func listMappings(ctx context.Context, c *client) ([]proto.Message, error) {
	r, err := c.ldap.ListLdapGroupMappings(ctx, connect.NewRequest(&relicv1.ListLdapGroupMappingsRequest{}))
	return messages(r, err, (*relicv1.ListLdapGroupMappingsResponse).GetLdapGroupMappings)
}

func putMapping(ctx context.Context, c *client, m proto.Message) error {
	_, err := c.ldap.PutLdapGroupMapping(
		ctx,
		connect.NewRequest(&relicv1.PutLdapGroupMappingRequest{
			LdapGroupMapping: m.(*relicv1.LdapGroupMapping),
		}),
	)

	return err
}

func listPolicies(ctx context.Context, c *client) ([]proto.Message, error) {
	r, err := c.scans.ListPolicies(ctx, connect.NewRequest(&relicv1.ListPoliciesRequest{}))
	return messages(r, err, (*relicv1.ListPoliciesResponse).GetPolicies)
}

func putPolicy(ctx context.Context, c *client, m proto.Message) error {
	_, err := c.scans.PutPolicy(ctx, connect.NewRequest(&relicv1.PutPolicyRequest{
		Policy: m.(*relicv1.Policy),
	}))

	return err
}

const maxCVSS = 10

func closeCVSSRanges(spec map[string]any) {
	rules, _ := spec["rules"].([]any)
	for _, r := range rules {
		rule, ok := r.(map[string]any)
		if !ok {
			continue
		}

		from := hasAnyKey(rule, "cvssFrom", "cvss_from")
		to := hasAnyKey(rule, "cvssTo", "cvss_to")

		switch {
		case from && !to:
			rule["cvssTo"] = maxCVSS
		case to && !from:
			rule["cvssFrom"] = 0
		}
	}
}

func hasAnyKey(m map[string]any, keys ...string) bool {
	for _, k := range keys {
		if _, ok := m[k]; ok {
			return true
		}
	}

	return false
}

func listWatches(ctx context.Context, c *client) ([]proto.Message, error) {
	r, err := c.scans.ListWatches(ctx, connect.NewRequest(&relicv1.ListWatchesRequest{}))
	return messages(r, err, (*relicv1.ListWatchesResponse).GetWatches)
}

func putWatch(ctx context.Context, c *client, m proto.Message) error {
	_, err := c.scans.PutWatch(ctx, connect.NewRequest(&relicv1.PutWatchRequest{
		Watch: m.(*relicv1.Watch),
	}))

	return err
}
