package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"relic/internal/app"
)

// version is the release this binary was built from, set by the release
// workflow (-ldflags "-X main.version=v1.2.3").
var version = "dev"

type globals struct {
	getenv      func(string) string
	config      string
	context     string
	url         string
	token       string
	machineRoot string
	progress    *commandProgress

	userConfigDir func() (string, error)
}

func newRootCmd(getenv func(string) string) *cobra.Command {
	return rootCmd(&globals{
		getenv: getenv,
	})
}

func rootCmd(g *globals) *cobra.Command {
	root := &cobra.Command{
		Use:           "relic",
		Short:         "Relic artifact repository: server, worker and command-line client",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().
		StringVar(&g.config, "config", "", "config file (default $RELIC_CONFIG or <user config dir>/relic/config.yaml)")
	root.PersistentFlags().StringVar(&g.context, "context", "", "context to use instead of the current one")
	root.PersistentFlags().StringVar(&g.url, "url", "", "Relic URL (overrides $RELIC_URL and the context)")
	root.PersistentFlags().StringVar(&g.token, "token", "", "access token (overrides $RELIC_TOKEN and the context)")

	root.AddGroup(
		&cobra.Group{
			ID:    "machine",
			Title: "This machine:",
		},
		&cobra.Group{
			ID:    "client",
			Title: "Using a Relic server:",
		},
	)

	server := group("server", "Run, install and inspect the Relic server on this machine", "machine",
		serviceCmd("start", "server", g.getenv),
		serviceCmd("worker", "worker", g.getenv),
		serviceCmd("migrate", "migrate", g.getenv),
		serverSetupCmd(g),
		serverStopCmd(g),
		uninstallCmd(g),
		serverStatusCmd(g),
		serverConfigCmd(g),
		databaseCmd(g.getenv),
	)
	root.AddCommand(server)

	for _, c := range []*cobra.Command{
		group("auth", "Sign in, switch contexts and manage access tokens", "",
			loginCmd(g),
			logoutCmd(g),
			authStatusCmd(g),
			contextCmd(g),
			tokenCmd(g),
		),
		group("artifact", "Upload, download, copy, move, delete, find and annotate files in repositories", "",
			uploadCmd(g),
			downloadCmd(g),
			copyMoveCmd(g, false),
			copyMoveCmd(g, true),
			removeCmd(g),
			searchCmd(g),
			propsCmd(g),
			lfsCmd(g),
		),
		applyCmd(g),
		diffCmd(g),
		getCmd(g),
		deleteCmd(g),
		buildCmd(g),
		releaseCmd(g),
		evidenceCmd(g),
		imageCmd(g),
		group("security", "Scan files and projects, read scan reports, exchange VEX and SBOMs and import the vulnerability database", "",
			scanCmd(g),
			auditCmd(g),
			reportCmd(g),
			vexCmd(g),
			sbomCmd(g),
			quarantineCmd(g),
			waiverCmd(g),
			policyCmd(g),
			decisionsCmd(g),
			risksCmd(g),
			vulnerabilityDatabaseCmd(g),
			endOfLifeCmd(g),
		),
		group("package", "Configure package managers and publish Go, Terraform and Hugging Face packages", "",
			packageSetupCmd(g),
			packageCheckCmd(g),
			publishCmd(g),
			huggingFaceCmd(g),
		),
		group("storage", "Check stored content against the database", "",
			storageCheckCmd(g),
		),
		group("hold", "Place and release legal holds, which protect items from deletion and change", "",
			holdAddCmd(g),
			holdRemoveCmd(g),
			holdListCmd(g),
		),
		mcpCmd(g),
		usageCmd(g),
		group("replication", "Run replications (configure them as relic.build/v1 Replication resources)", "",
			replicationRunCmd(g),
		),
		group("registry", "List and approve the entries of MCP server and agent skill registries", "",
			registryListCmd(g),
			registryDecideCmd(g, true),
			registryDecideCmd(g, false),
		),
		group("project", "Manage project members (projects themselves are relic.build/v1 Project resources)", "",
			projectMemberAddCmd(g),
			projectMemberRemoveCmd(g),
		),
		group("webhook", "Send Relic's events to other services: list deliveries and test webhooks", "",
			webhookDeliveriesCmd(g),
			webhookPingCmd(g),
		),
		group("audit", "Read the audit log", "",
			auditListCmd(g),
		),
		group("cleanup", "Preview and run cleanup policies", "",
			cleanupPreviewCmd(g),
			cleanupRunCmd(g),
		),
		importCmd(g),
		apiCmd(g),
	} {
		c.GroupID = "client"
		root.AddCommand(c)
	}

	g.showProgress(root)

	return root
}

func group(use, short, groupID string, cmds ...*cobra.Command) *cobra.Command {
	cmd := &cobra.Command{
		Use:     use,
		Short:   short,
		GroupID: groupID,
	}
	cmd.AddCommand(cmds...)

	return cmd
}

func serviceCmd(use, process string, getenv func(string) string) *cobra.Command {
	short := map[string]string{
		"server":  "Run the Relic server (with --worker, the background jobs too)",
		"worker":  "Run the background job worker",
		"migrate": "Apply database migrations and exit",
	}[process]

	var (
		withWorker, withoutTLS bool
		settings               string
	)

	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		Annotations: map[string]string{
			progressAnnotation: "off",
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			if settings != "" {
				if err := loadSettings(settings); err != nil {
					return err
				}
			}

			args := []string{
				process,
			}
			if withWorker {
				args = append(args, "--worker")
			}

			if withoutTLS {
				args = append(args, "--no-tls")
			}

			if version != "dev" {
				app.Version = version
			}

			return app.Run(cmd.Context(), args, getenv, cmd.ErrOrStderr())
		},
	}

	if process == "server" {
		cmd.Flags().BoolVar(
			&withWorker,
			"worker",
			false,
			"also run the background jobs in this process (a single node; otherwise run relic server worker beside it)",
		)
		cmd.Flags().BoolVar(
			&withoutTLS,
			"no-tls",
			false,
			"serve plain HTTP instead of HTTPS (behind a proxy that terminates TLS)",
		)
		cmd.Flags().StringVar(
			&settings,
			"settings",
			"",
			"a file of KEY=value lines to set in the environment first, over the variables already set",
		)
	}

	return cmd
}

func loadSettings(path string) error {
	b, err := os.ReadFile(path) //nolint:gosec // the settings file the operator names
	if err != nil {
		return fmt.Errorf("settings: %w", err)
	}

	for key, value := range keyValues(string(b), "=") {
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("settings: %s: %w", key, err)
		}
	}

	return nil
}
