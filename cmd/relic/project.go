package main

import (
	"fmt"

	"github.com/spf13/cobra"

	relicv1 "relic/api/gen/relic/v1"
)

var projectRoleNames = map[string]relicv1.ProjectRole{
	"admin":     relicv1.ProjectRole_PROJECT_ROLE_ADMIN,
	"developer": relicv1.ProjectRole_PROJECT_ROLE_DEVELOPER,
	"viewer":    relicv1.ProjectRole_PROJECT_ROLE_VIEWER,
}

func projectMemberAddCmd(g *globals) *cobra.Command {
	var (
		role  string
		group bool
	)

	cmd := &cobra.Command{
		Use:     "member-add <project> <user-or-group>",
		Short:   "Add a member to a project, or change a member's role",
		Example: "  relic project member-add acme alice --role developer\n  relic project member-add acme qa --group --role viewer",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, ok := projectRoleNames[role]
			if !ok {
				return fmt.Errorf("unknown role %q: admin, developer or viewer", role)
			}

			c, err := g.dial()
			if err != nil {
				return err
			}

			res, err := c.projects.AddProjectMember(
				cmd.Context(),
				&relicv1.AddProjectMemberRequest{
					Key: args[0],
					Member: &relicv1.ProjectMember{
						Name:  args[1],
						Group: group,
						Role:  r,
					},
				},
			)
			if err != nil {
				return fmt.Errorf("add %s to %s: %w", args[1], args[0], err)
			}

			return printProjectMemberCount(cmd, res.GetProject())
		},
	}
	cmd.Flags().StringVar(&role, "role", "developer", "admin, developer or viewer")
	cmd.Flags().BoolVar(&group, "group", false, "the member is a group")
	_ = cmd.RegisterFlagCompletionFunc(
		"role",
		cobra.FixedCompletions(
			[]string{
				"admin",
				"developer",
				"viewer",
			},
			cobra.ShellCompDirectiveNoFileComp,
		),
	)

	return cmd
}

func projectMemberRemoveCmd(g *globals) *cobra.Command {
	var group bool

	cmd := &cobra.Command{
		Use:     "member-remove <project> <user-or-group>",
		Short:   "Remove a member from a project",
		Example: "  relic project member-remove acme alice",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.dial()
			if err != nil {
				return err
			}

			res, err := c.projects.RemoveProjectMember(
				cmd.Context(),
				&relicv1.RemoveProjectMemberRequest{
					Key:   args[0],
					Name:  args[1],
					Group: group,
				},
			)
			if err != nil {
				return fmt.Errorf("remove %s from %s: %w", args[1], args[0], err)
			}

			return printProjectMemberCount(cmd, res.GetProject())
		},
	}
	cmd.Flags().BoolVar(&group, "group", false, "the member is a group")

	return cmd
}

func printProjectMemberCount(cmd *cobra.Command, p *relicv1.Project) error {
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s has %d members\n", p.GetKey(), len(p.GetMembers()))

	return err
}
