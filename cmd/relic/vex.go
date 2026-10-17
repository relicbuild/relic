package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	relicv1 "relic/api/gen/relic/v1"

	"relic/internal/platform/terminal"
)

func vexCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vex",
		Short: "Exchange ignore rules as OpenVEX or CycloneDX VEX documents (administrators)",
		Long: "VEX says which vulnerabilities do not affect which packages. relic security vex export writes Relic's " +
			"ignore rules as such a document, in OpenVEX or CycloneDX, for scanners and consumers downstream; relic " +
			"security vex import turns a supplier's or another team's document, in either format, into ignore rules.",
	}
	cmd.AddCommand(
		vexExportCmd(g),
		vexImportCmd(g),
	)

	return cmd
}

func vexExportCmd(g *globals) *cobra.Command {
	var (
		file, format string
		unreachable  bool
	)

	cmd := &cobra.Command{
		Use:   "export",
		Short: "Write the ignore rules as an OpenVEX or CycloneDX document",
		Example: "  relic security vex export > relic.vex.json\n" +
			"  relic security vex export --format cyclonedx --unreachable --file relic.cdx.json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			f, ok := map[string]relicv1.VexFormat{
				"openvex":   relicv1.VexFormat_VEX_FORMAT_OPENVEX,
				"cyclonedx": relicv1.VexFormat_VEX_FORMAT_CYCLONEDX,
			}[format]
			if !ok {
				return fmt.Errorf("unknown format %q: openvex or cyclonedx", format)
			}

			c, err := g.dial()
			if err != nil {
				return err
			}

			r, err := c.scans.ExportVex(cmd.Context(), connect.NewRequest(&relicv1.ExportVexRequest{
				Format:             f,
				IncludeUnreachable: unreachable,
			}))
			if err != nil {
				return fmt.Errorf("vex export: %w", err)
			}

			if r.Msg.GetSkipped() > 0 {
				_, _ = fmt.Fprintf(
					cmd.ErrOrStderr(),
					"%d ignore rules left out: expired, or without both a vulnerability and a package\n",
					r.Msg.GetSkipped(),
				)
			}

			if file == "" {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), r.Msg.GetDocument())

				return err
			}

			return writeFileAtomic(file, strings.NewReader(r.Msg.GetDocument()+"\n"), 0o644)
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "write the document to this file instead of standard output")
	cmd.Flags().StringVar(&format, "format", "openvex", "openvex or cyclonedx")
	cmd.Flags().BoolVar(
		&unreachable,
		"unreachable",
		false,
		"also state the Go vulnerabilities govulncheck found no call path to, per scanned file",
	)
	_ = cmd.RegisterFlagCompletionFunc(
		"format",
		cobra.FixedCompletions(
			[]string{
				"openvex",
				"cyclonedx",
			},
			cobra.ShellCompDirectiveNoFileComp,
		),
	)

	return cmd
}

func vexImportCmd(g *globals) *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "import <document>",
		Short: "Create ignore rules from an OpenVEX or CycloneDX document's not_affected and fixed (resolved) statements",
		Long: "Each rule ignores the statement's vulnerability in the packages it names, and its notes name the " +
			"document and its author. - reads standard input.",
		Example: "  relic security vex import vendor.vex.json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.dial()
			if err != nil {
				return err
			}

			var b []byte

			if args[0] == "-" {
				b, err = io.ReadAll(cmd.InOrStdin())
			} else {
				b, err = os.ReadFile(args[0])
			}

			if err != nil {
				return err
			}

			r, err := c.scans.ImportVex(cmd.Context(), connect.NewRequest(&relicv1.ImportVexRequest{
				Document: string(b),
			}))
			if err != nil {
				return fmt.Errorf("vex import: %w", err)
			}

			return printMessage(cmd.OutOrStdout(), output, r.Msg, func(w io.Writer) error {
				table := terminal.Table{
					Headers: []string{
						"IGNORE RULE",
						"VULNERABILITIES",
						"PACKAGES",
					},
				}

				for _, rule := range r.Msg.GetRules() {
					table.Rows = append(table.Rows, []string{
						rule.GetId(),
						strings.Join(rule.GetVulnerabilities(), ", "),
						strings.Join(rule.GetPackages(), ", "),
					})
				}

				if len(table.Rows) > 0 {
					if err := table.Print(w); err != nil {
						return err
					}
				}

				_, err := fmt.Fprintf(
					w,
					"%d ignore rules created, %d statements left out\n",
					len(r.Msg.GetRules()),
					r.Msg.GetSkipped(),
				)

				return err
			})
		},
	}
	outputFlag(cmd, &output)

	return cmd
}
