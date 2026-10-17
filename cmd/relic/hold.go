package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/platform/terminal"
)

func splitItemPath(arg string) (string, string, error) {
	repo, path, _ := strings.Cut(strings.Trim(arg, "/"), "/")
	if repo == "" || path == "" {
		return "", "", fmt.Errorf("%q: want <repository>/<path>", arg)
	}

	return repo, path, nil
}

func holdAddCmd(g *globals) *cobra.Command {
	var reason string

	cmd := &cobra.Command{
		Use:   "add <repository>/<path>",
		Short: "Place a legal hold on an item, or a folder with everything under it",
		Long: "A held item cannot be deleted, moved, overwritten or cleaned up until the hold is released. " +
			"Administrators only.",
		Example: "  relic hold add libs-local/org/app/1.4.0 --reason \"audit 2026-14\"",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, path, err := splitItemPath(args[0])
			if err != nil {
				return err
			}

			c, err := g.dial()
			if err != nil {
				return err
			}

			if _, err := c.holds.PutHold(cmd.Context(), connect.NewRequest(&relicv1.PutHoldRequest{
				Repository: repo,
				Path:       path,
				Reason:     reason,
			})); err != nil {
				return fmt.Errorf("hold %s: %w", args[0], err)
			}

			_, err = fmt.Fprintf(cmd.OutOrStdout(), "held %s\n", args[0])

			return err
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "why the item is held")
	_ = cmd.MarkFlagRequired("reason")

	return cmd
}

func holdRemoveCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:     "remove <repository>/<path>",
		Short:   "Release the legal hold placed on an item",
		Example: "  relic hold remove libs-local/org/app/1.4.0",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, path, err := splitItemPath(args[0])
			if err != nil {
				return err
			}

			c, err := g.dial()
			if err != nil {
				return err
			}

			if _, err := c.holds.DeleteHold(cmd.Context(), connect.NewRequest(&relicv1.DeleteHoldRequest{
				Repository: repo,
				Path:       path,
			})); err != nil {
				return fmt.Errorf("release %s: %w", args[0], err)
			}

			_, err = fmt.Fprintf(cmd.OutOrStdout(), "released %s\n", args[0])

			return err
		},
	}
}

func holdListCmd(g *globals) *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List legal holds",
		Example: "  relic hold list\n  relic hold list -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := g.dial()
			if err != nil {
				return err
			}

			res, err := c.holds.ListHolds(cmd.Context(), connect.NewRequest(&relicv1.ListHoldsRequest{}))
			if err != nil {
				return fmt.Errorf("list holds: %w", err)
			}

			return printMessage(cmd.OutOrStdout(), output, res.Msg, func(w io.Writer) error {
				table := terminal.Table{
					Headers: []string{
						"ITEM",
						"REASON",
						"BY",
						"SINCE",
					},
				}

				for _, h := range res.Msg.GetHolds() {
					table.Rows = append(table.Rows, []string{
						h.GetRepository() + "/" + h.GetPath(),
						tableCell(h.GetReason()),
						h.GetCreatedBy(),
						h.GetCreated().AsTime().Local().Format(time.DateOnly),
					})
				}

				return table.Print(w)
			})
		},
	}
	outputFlag(cmd, &output)

	return cmd
}
