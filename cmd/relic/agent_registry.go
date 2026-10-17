package main

import (
	"fmt"
	"io"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/platform/terminal"
)

func registryListCmd(g *globals) *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:     "list <repository>",
		Short:   "List an mcp or skills repository's entries (unapproved ones to administrators and viewers)",
		Example: "  relic registry list mcp-servers\n  relic registry list agent-skills -o json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.dial()
			if err != nil {
				return err
			}

			res, err := c.agentRegistry.ListRegistryEntries(
				cmd.Context(),
				connect.NewRequest(&relicv1.ListRegistryEntriesRequest{
					Repository: args[0],
				}),
			)
			if err != nil {
				return fmt.Errorf("list %s: %w", args[0], err)
			}

			return printMessage(cmd.OutOrStdout(), output, res.Msg, func(w io.Writer) error {
				table := terminal.Table{
					Headers: []string{
						"NAME",
						"VERSION",
						"APPROVED",
						"PATH",
						"DESCRIPTION",
					},
				}

				for _, e := range res.Msg.GetEntries() {
					approved := "no"
					if e.GetApproved() {
						approved = "by " + e.GetApprovedBy()
					}

					table.Rows = append(table.Rows, []string{
						e.GetName(),
						e.GetVersion(),
						approved,
						e.GetPath(),
						tableCell(e.GetDescription()),
					})
				}

				return table.Print(w)
			})
		},
	}
	outputFlag(cmd, &output)

	return cmd
}

func registryDecideCmd(g *globals, approve bool) *cobra.Command {
	verb, done, short := "revoke", "revoked", "Withdraw an entry's approval"
	if approve {
		verb, done, short = "approve", "approved", "Approve an entry's current content, so the registry serves it"
	}

	return &cobra.Command{
		Use:     verb + " <repository>/<path>",
		Short:   short,
		Example: "  relic registry " + verb + " mcp-servers/weather/1.1.0/server.json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, p, err := splitTarget(args[0])
			if err != nil {
				return err
			}

			c, err := g.dial()
			if err != nil {
				return err
			}

			if approve {
				_, err = c.agentRegistry.ApproveRegistryEntry(
					cmd.Context(),
					connect.NewRequest(&relicv1.ApproveRegistryEntryRequest{
						Repository: repo,
						Path:       p,
					}),
				)
			} else {
				_, err = c.agentRegistry.RevokeRegistryApproval(
					cmd.Context(),
					connect.NewRequest(&relicv1.RevokeRegistryApprovalRequest{
						Repository: repo,
						Path:       p,
					}),
				)
			}

			if err != nil {
				return fmt.Errorf("%s %s: %w", verb, args[0], err)
			}

			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", done, args[0])

			return err
		},
	}
}
