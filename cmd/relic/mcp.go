package main

import (
	"io"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"relic/api/mcpserver"
)

func mcpCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Serve Relic's read-only tools to an AI agent over MCP on standard input and output",
		Long: "Runs a Model Context Protocol server on standard input and output, for agents that start it\n" +
			"as a subprocess. Its tools call the Relic API with this context's credentials, so the agent sees\n" +
			"only what you may read. Servers also serve MCP over HTTP at /mcp.",
		Example: "  relic mcp\n  relic --context ci mcp",
		Args:    cobra.NoArgs,
		Annotations: map[string]string{
			progressAnnotation: "off",
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := g.dial()
			if err != nil {
				return err
			}

			srv := mcpserver.NewServer(mcpserver.NewClients(c.http, c.url+"/api"), version)

			return srv.Run(cmd.Context(), &mcp.IOTransport{
				Reader: io.NopCloser(cmd.InOrStdin()),
				Writer: nopWriteCloser{
					Writer: cmd.OutOrStdout(),
				},
			})
		},
	}
}

type nopWriteCloser struct {
	io.Writer
}

func (nopWriteCloser) Close() error { return nil }
