package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/platform/terminal"
)

var operationalRiskKinds = []string{
	"end_of_life",
	"deprecated",
	"unmaintained",
}

func risksCmd(g *globals) *cobra.Command {
	var (
		kind, query, repository, output string
		limit                           int32
	)

	cmd := &cobra.Command{
		Use:   "risks",
		Short: "List operational risks in stored files: components past or near end of life, deprecated or unmaintained",
		Example: "  relic security risks\n  relic security risks --kind end_of_life --repository libs-local\n" +
			"  relic security risks --query django -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if kind != "" && !slices.Contains(operationalRiskKinds, kind) {
				return fmt.Errorf("unknown kind %q: %s", kind, strings.Join(operationalRiskKinds, ", "))
			}

			c, err := g.dial()
			if err != nil {
				return err
			}

			res, err := c.scans.ListOperationalRisks(
				cmd.Context(),
				connect.NewRequest(&relicv1.ListOperationalRisksRequest{
					Kind:       kind,
					Query:      query,
					Repository: repository,
					PageSize:   limit,
				}),
			)
			if err != nil {
				return fmt.Errorf("list operational risks: %w", err)
			}

			return printMessage(cmd.OutOrStdout(), output, res.Msg, func(w io.Writer) error {
				table := terminal.Table{
					Headers: []string{
						"SEVERITY",
						"KIND",
						"PACKAGE",
						"VERSION",
						"FILE",
						"DETAIL",
					},
				}

				for _, r := range res.Msg.GetRisks() {
					table.Rows = append(table.Rows, []string{
						strings.TrimPrefix(strings.ToLower(r.GetSeverity().String()), "severity_"),
						r.GetKind(),
						r.GetPackageName(),
						r.GetPackageVersion(),
						r.GetRepository() + "/" + r.GetPath(),
						tableCell(r.GetDetail()),
					})
				}

				if err := table.Print(w); err != nil {
					return err
				}

				_, err := fmt.Fprintf(w, "%d of %d\n", len(res.Msg.GetRisks()), res.Msg.GetTotal())

				return err
			})
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "end_of_life, deprecated or unmaintained")
	cmd.Flags().StringVar(&query, "query", "", "match package and product names")
	cmd.Flags().StringVar(&repository, "repository", "", "files in this repository only")
	cmd.Flags().Int32Var(&limit, "limit", 100, "the most risks to list")
	_ = cmd.RegisterFlagCompletionFunc(
		"kind",
		cobra.FixedCompletions(
			operationalRiskKinds,
			cobra.ShellCompDirectiveNoFileComp,
		),
	)
	outputFlag(cmd, &output)

	return cmd
}

func endOfLifeCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "end-of-life",
		Short: "Import the end-of-life data Relic Scan assesses components with, and show what the server holds",
		Long: "The worker reads endoflife.date's product data every day, unless RELIC_END_OF_LIFE_URL is off: " +
			"an installation without Internet access imports a copy of https://endoflife.date/api/v1/products/full.",
	}
	cmd.AddCommand(
		endOfLifeImportCmd(g),
		endOfLifeStatusCmd(g),
	)

	return cmd
}

func endOfLifeImportCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "import <file>",
		Short: "Replace the end-of-life data with a copy of endoflife.date's product dump (administrators)",
		Example: "  # on a connected machine\n  curl -fsSLo eol.json https://endoflife.date/api/v1/products/full\n" +
			"  # inside\n  relic security end-of-life import eol.json",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := os.Open(args[0])
			if err != nil {
				return err
			}

			defer func() {
				_ = f.Close()
			}()

			c, err := g.dial()
			if err != nil {
				return err
			}

			req, err := http.NewRequestWithContext(cmd.Context(), http.MethodPut, c.url+"/api/v1/end-of-life-data", f)
			if err != nil {
				return err
			}

			req.Header.Set("Content-Type", "application/json")

			resp, err := c.http.Do(req)
			if err != nil {
				return err
			}

			defer func() {
				_ = resp.Body.Close()
			}()

			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("end-of-life import: %w", httpError(resp))
			}

			_, _ = fmt.Fprintf(
				cmd.OutOrStdout(),
				"%s imported; the worker assesses stored scans again (relic security risks)\n",
				args[0],
			)

			return nil
		},
	}
}

func endOfLifeStatusCmd(g *globals) *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show where the end-of-life data came from and when",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := g.dial()
			if err != nil {
				return err
			}

			r, err := c.scans.GetEndOfLifeStatus(
				cmd.Context(),
				connect.NewRequest(&relicv1.GetEndOfLifeStatusRequest{}),
			)
			if err != nil {
				return fmt.Errorf("end-of-life status: %w", err)
			}

			s := r.Msg

			return printMessage(cmd.OutOrStdout(), output, s, func(w io.Writer) error {
				if s.GetSource() == "" {
					_, err := fmt.Fprintln(w, "no end-of-life data yet")

					return err
				}

				_, err := fmt.Fprintf(
					w,
					"%d products, %s, changed %s, read %s\n",
					s.GetProducts(),
					s.GetSource(),
					s.GetChanged().AsTime().Local().Format(time.DateTime),
					s.GetFetched().AsTime().Local().Format(time.DateTime),
				)

				return err
			})
		},
	}
	outputFlag(cmd, &output)

	return cmd
}
