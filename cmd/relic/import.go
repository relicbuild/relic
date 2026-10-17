package main

import (
	"github.com/spf13/cobra"

	migration "relic/api/adapters/artifactory/transfer"
)

func importCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import repositories, security configuration and files from another repository manager",
	}
	cmd.AddCommand(migration.Command(func() (migration.Server, error) {
		c, err := g.dial()
		if err != nil {
			return migration.Server{}, err
		}

		return migration.Server{
			URL:    c.url,
			Client: c.http,
		}, nil
	}, func(step string) {
		g.progress.describe(step)
	}))

	return cmd
}
