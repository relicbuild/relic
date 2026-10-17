package main

import (
	"fmt"
	"io"
	"strconv"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/platform/terminal"
)

func usageCmd(g *globals) *cobra.Command {
	var (
		days   int32
		output string
	)

	cmd := &cobra.Command{
		Use:     "usage",
		Short:   "Report storage, growth, downloads and deploys by repository, project and user",
		Example: "  relic usage\n  relic usage --days 90 -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := g.dial()
			if err != nil {
				return err
			}

			res, err := c.system.GetUsageReport(cmd.Context(), connect.NewRequest(&relicv1.GetUsageReportRequest{
				Days: days,
			}))
			if err != nil {
				return fmt.Errorf("usage report: %w", err)
			}

			return printMessage(cmd.OutOrStdout(), output, res.Msg, func(w io.Writer) error {
				return printUsage(w, res.Msg)
			})
		},
	}
	cmd.Flags().Int32Var(&days, "days", 30, "the period, today included, at most 730")
	outputFlag(cmd, &output)

	return cmd
}

func printUsage(w io.Writer, rep *relicv1.GetUsageReportResponse) error {
	if _, err := fmt.Fprintf(w, "%s to %s\n\n", rep.GetFrom(), rep.GetTo()); err != nil {
		return err
	}

	table := terminal.Table{
		Headers: []string{
			"REPOSITORY",
			"PROJECT",
			"FILES",
			"SIZE",
			"GROWTH",
			"DOWNLOADS",
			"DEPLOYS",
		},
		RightAligned: []int{
			2,
			3,
			4,
			5,
			6,
		},
	}

	for _, r := range rep.GetRepositories() {
		table.Rows = append(table.Rows, []string{
			r.GetRepository(),
			r.GetProject(),
			strconv.FormatInt(r.GetFiles(), 10),
			terminal.ByteSize(r.GetBytes()),
			terminal.ByteSize(r.GetGrowthBytes()),
			strconv.FormatInt(r.GetDownloads(), 10),
			strconv.FormatInt(r.GetDeploys(), 10),
		})
	}

	if err := table.Print(w); err != nil {
		return err
	}

	for _, top := range []struct {
		title  string
		column string
		users  []*relicv1.UserUsage
		count  func(*relicv1.UserUsage) int64
	}{
		{
			title:  "TOP DOWNLOADERS",
			column: "DOWNLOADS",
			users:  rep.GetTopDownloaders(),
			count:  (*relicv1.UserUsage).GetDownloads,
		},
		{
			title:  "TOP DEPLOYERS",
			column: "DEPLOYS",
			users:  rep.GetTopDeployers(),
			count:  (*relicv1.UserUsage).GetDeploys,
		},
	} {
		if len(top.users) == 0 {
			continue
		}

		users := terminal.Table{
			Headers: []string{
				top.title,
				top.column,
			},
			RightAligned: []int{
				1,
			},
		}

		for _, u := range top.users {
			users.Rows = append(users.Rows, []string{
				u.GetUser(),
				strconv.FormatInt(top.count(u), 10),
			})
		}

		if err := users.Print(w); err != nil {
			return err
		}
	}

	return nil
}
