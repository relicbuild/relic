package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/platform/terminal"
)

func quarantineCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "quarantine",
		Short: "List, release and reject the files quarantined remote repositories hold",
	}
	cmd.AddCommand(quarantineListCmd(g), quarantineDecideCmd(g, true), quarantineDecideCmd(g, false))

	return cmd
}

func quarantineListCmd(g *globals) *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List the files quarantined remote repositories hold",
		Example: "  relic security quarantine list\n  relic security quarantine list -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := g.dial()
			if err != nil {
				return err
			}

			res, err := c.quarantine.ListQuarantined(
				cmd.Context(),
				&relicv1.ListQuarantinedRequest{},
			)
			if err != nil {
				return fmt.Errorf("list quarantine: %w", err)
			}

			return printMessage(cmd.OutOrStdout(), output, res, func(w io.Writer) error {
				table := terminal.Table{
					Headers: []string{
						"FILE",
						"STATE",
						"CACHED",
						"REASON",
					},
				}

				for _, f := range res.GetFiles() {
					table.Rows = append(table.Rows, []string{
						f.GetRepository() + "/" + f.GetPath(),
						strings.ToLower(strings.TrimPrefix(f.GetState().String(), "QUARANTINE_STATE_")),
						f.GetCached().AsTime().Local().Format(time.DateTime),
						tableCell(f.GetReason()),
					})
				}

				return table.Print(w)
			})
		},
	}
	outputFlag(cmd, &output)

	return cmd
}

func quarantineDecideCmd(g *globals, release bool) *cobra.Command {
	var reason string

	verb, done, short := "reject", "rejected", "Never serve a held file, whatever its scan says"
	if release {
		verb, done, short = "release", "released", "Serve a held file, whatever its scan says"
	}

	cmd := &cobra.Command{
		Use:               verb + " <repository>/<path>",
		Short:             short,
		Example:           "  relic security quarantine " + verb + " maven-remote/org/acme/lib/1.0/lib-1.0.jar --reason \"reviewed\"",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeTargetArg(g, 0),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, p, err := splitTarget(args[0])
			if err != nil {
				return err
			}

			c, err := g.dial()
			if err != nil {
				return err
			}

			if release {
				_, err = c.quarantine.ReleaseQuarantined(
					cmd.Context(),
					&relicv1.ReleaseQuarantinedRequest{
						Repository: repo,
						Path:       p,
						Reason:     reason,
					},
				)
			} else {
				_, err = c.quarantine.RejectQuarantined(
					cmd.Context(),
					&relicv1.RejectQuarantinedRequest{
						Repository: repo,
						Path:       p,
						Reason:     reason,
					},
				)
			}

			if err != nil {
				return fmt.Errorf("%s %s: %w", verb, args[0], err)
			}

			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", done, args[0])

			return err
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "why, for the audit log and the quarantine list")

	return cmd
}
