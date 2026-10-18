package main

import (
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	relicv1 "relic/api/gen/relic/v1"

	"relic/internal/platform/terminal"
)

var tokenScopes = map[string]relicv1.TokenScopeKind{
	"user":   relicv1.TokenScopeKind_TOKEN_SCOPE_KIND_USER,
	"groups": relicv1.TokenScopeKind_TOKEN_SCOPE_KIND_GROUPS,
	"admin":  relicv1.TokenScopeKind_TOKEN_SCOPE_KIND_ADMIN,
}

func tokenCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Create, list and revoke access tokens",
	}
	cmd.AddCommand(
		tokenCreateCmd(g),
		tokenListCmd(g),
		tokenRevokeCmd(g),
	)

	return cmd
}

func tokenCreateCmd(g *globals) *cobra.Command {
	var (
		description string
		expiresDays int32
		scope       string
		groups      []string
		project     string
		output      string
	)

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create an access token for yourself",
		Long: "Creates an access token and prints it (only now: Relic keeps no copy). " +
			"The token works with the Relic API, the relic CLI and every package manager client. " +
			"--scope groups limits it to the groups named with --group; only administrators may create admin tokens.",
		Example: "  export RELIC_TOKEN=$(relic auth token create --description ci --expires-days 30)\n" +
			"  relic auth token create --scope groups --group deployers -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			kind, ok := tokenScopes[scope]
			if !ok {
				return fmt.Errorf("unknown scope %q: use user, groups or admin", scope)
			}

			if len(groups) > 0 && kind == relicv1.TokenScopeKind_TOKEN_SCOPE_KIND_USER {
				kind = relicv1.TokenScopeKind_TOKEN_SCOPE_KIND_GROUPS
			}

			c, err := g.dial()
			if err != nil {
				return err
			}

			r, err := c.security.CreateToken(
				cmd.Context(),
				&relicv1.CreateTokenRequest{
					Description: description,
					ExpiresDays: expiresDays,
					Scope: &relicv1.TokenScope{
						Kind:   kind,
						Groups: groups,
					},
					Project: project,
				},
			)
			if err != nil {
				return fmt.Errorf("token create: %w", err)
			}

			return printMessage(cmd.OutOrStdout(), output, r, func(w io.Writer) error {
				_, err := fmt.Fprintln(w, r.GetAccessToken())

				return err
			})
		},
	}
	cmd.Flags().StringVar(&description, "description", "", "what the token is for")
	cmd.Flags().
		Int32Var(&expiresDays, "expires-days", 0, "lifetime in days, at most 3650 (0: never expires for administrators; other users' tokens expire within 365)")
	cmd.Flags().StringVar(&scope, "scope", "user", "what the token acts as: user, groups or admin")
	cmd.Flags().StringSliceVar(&groups, "group", nil, "group the token acts with (implies --scope groups; repeatable)")
	cmd.Flags().
		StringVar(&project, "project", "", "a project to label the token with; it does not limit what the token can do")
	_ = cmd.RegisterFlagCompletionFunc(
		"scope",
		cobra.FixedCompletions(
			[]string{
				"user",
				"groups",
				"admin",
			},
			cobra.ShellCompDirectiveNoFileComp,
		),
	)
	outputFlag(cmd, &output)

	return cmd
}

func tokenListCmd(g *globals) *cobra.Command {
	var (
		user    string
		revoked bool
		output  string
	)

	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List access tokens (without their secrets)",
		Long:    "Lists your tokens; administrators and viewers see everyone's, or one user's with --user.",
		Example: "  relic auth token list\n  relic auth token list --user ci-bot -o yaml",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := g.dial()
			if err != nil {
				return err
			}

			r, err := c.security.ListTokens(
				cmd.Context(),
				&relicv1.ListTokensRequest{
					Username:       user,
					IncludeRevoked: revoked,
				},
			)
			if err != nil {
				return fmt.Errorf("token list: %w", err)
			}

			return printMessage(cmd.OutOrStdout(), output, r, func(w io.Writer) error {
				table := terminal.Table{
					Headers: []string{
						"ID",
						"USER",
						"EXPIRES",
						"STATE",
						"DESCRIPTION",
					},
				}

				now := time.Now()
				for _, t := range r.GetTokens() {
					table.Rows = append(table.Rows, tokenRow(t, now))
				}

				return table.Print(w)
			})
		},
	}
	cmd.Flags().StringVar(&user, "user", "", "list this user's tokens (administrators and viewers)")
	cmd.Flags().BoolVar(&revoked, "include-revoked", false, "list revoked tokens too")
	outputFlag(cmd, &output)

	return cmd
}

const tokenExpiresLayout = "2006-01-02 15:04"

func tokenRow(t *relicv1.AccessToken, now time.Time) []string {
	expires, state := "never", "active"
	if e := t.GetExpires(); e != nil {
		expires = localTime(e, tokenExpiresLayout)
		if !e.AsTime().After(now) {
			state = "expired"
		}
	}

	if t.GetRevoked() {
		state = "revoked"
	}

	return []string{
		t.GetId(),
		t.GetUsername(),
		expires,
		state,
		tableCell(t.GetDescription()),
	}
}

func tokenRevokeCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:     "revoke <id>...",
		Short:   "Revoke access tokens",
		Example: "  relic auth token revoke 01a14acc-9a25-73a0-b25c-c7f3bb3ab7a5",
		Args:    cobra.MinimumNArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
			c, err := g.dial()
			if err != nil {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}

			r, err := c.security.ListTokens(cmd.Context(), &relicv1.ListTokensRequest{})
			if err != nil {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}

			var out []string
			for _, t := range r.GetTokens() {
				out = append(out, t.GetId()+"\t"+t.GetDescription())
			}

			return out, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.dial()
			if err != nil {
				return err
			}

			for _, id := range args {
				if _, err := c.security.RevokeToken(
					cmd.Context(),
					&relicv1.RevokeTokenRequest{
						Id: id,
					},
				); err != nil {
					return fmt.Errorf("token revoke %s: %w", id, err)
				}

				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s revoked\n", id)
			}

			return nil
		},
	}
}
