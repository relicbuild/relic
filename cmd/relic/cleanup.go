package main

import (
	"fmt"
	"io"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/platform/terminal"
)

func cleanupPreviewCmd(g *globals) *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "preview <policy>",
		Short: "List what a cleanup policy would move to the trash now",
		Long: "Lists the items a stored cleanup policy selects, without moving anything. " +
			"Create policies with relic apply (kind CleanupPolicy).",
		Example: "  relic cleanup preview old-snapshots\n  relic cleanup preview old-snapshots -o json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.dial()
			if err != nil {
				return err
			}

			res, err := c.cleanup.PreviewCleanupPolicy(
				cmd.Context(),
				connect.NewRequest(&relicv1.PreviewCleanupPolicyRequest{
					Name: args[0],
				}),
			)
			if err != nil {
				return fmt.Errorf("preview %s: %w", args[0], err)
			}

			return printMessage(cmd.OutOrStdout(), output, res.Msg, func(w io.Writer) error {
				table := terminal.Table{
					Headers: []string{
						"REPOSITORY",
						"PATH",
						"SIZE",
						"CREATED",
					},
					RightAligned: []int{
						2,
					},
				}

				for _, cand := range res.Msg.GetCandidates() {
					table.Rows = append(table.Rows, []string{
						cand.GetRepository(),
						cand.GetPath(),
						terminal.ByteSize(cand.GetSize()),
						cand.GetCreated().AsTime().Local().Format(time.DateOnly),
					})
				}

				return table.Print(w)
			})
		},
	}
	outputFlag(cmd, &output)

	return cmd
}

func cleanupRunCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run <policy>",
		Short: "Move what a cleanup policy selects to the trash now",
		Long: "Runs a stored cleanup policy at once instead of waiting for the worker's daily run. " +
			"Items go to the trash, where they stay for the trash retention.",
		Example: "  relic cleanup run old-snapshots",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.dial()
			if err != nil {
				return err
			}

			res, err := c.cleanup.RunCleanupPolicy(cmd.Context(), connect.NewRequest(&relicv1.RunCleanupPolicyRequest{
				Name: args[0],
			}))
			if err != nil {
				return fmt.Errorf("run %s: %w", args[0], err)
			}

			_, err = fmt.Fprintf(cmd.OutOrStdout(), "moved %d items to the trash\n", res.Msg.GetMoved())

			return err
		},
	}

	return cmd
}
