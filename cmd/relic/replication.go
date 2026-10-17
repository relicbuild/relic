package main

import (
	"fmt"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	relicv1 "relic/api/gen/relic/v1"
)

func replicationRunCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:     "run <repository>",
		Short:   "Queue a run of a repository's replication now, whatever its schedule",
		Example: "  relic replication run libs-release",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.dial()
			if err != nil {
				return err
			}

			if _, err := c.replications.RunReplication(cmd.Context(), connect.NewRequest(&relicv1.RunReplicationRequest{
				Repository: args[0],
			})); err != nil {
				return fmt.Errorf("run the replication of %s: %w", args[0], err)
			}

			_, err = fmt.Fprintf(cmd.OutOrStdout(), "queued the replication of %s\n", args[0])

			return err
		},
	}
}
