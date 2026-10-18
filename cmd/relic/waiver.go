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

var waiverStatusNames = map[string]relicv1.WaiverStatus{
	"":         relicv1.WaiverStatus_WAIVER_STATUS_UNSPECIFIED,
	"pending":  relicv1.WaiverStatus_WAIVER_STATUS_PENDING,
	"approved": relicv1.WaiverStatus_WAIVER_STATUS_APPROVED,
	"rejected": relicv1.WaiverStatus_WAIVER_STATUS_REJECTED,
}

func waiverCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "waiver",
		Short: "Request ignore rules, and approve or reject the requests",
	}
	cmd.AddCommand(
		waiverRequestCmd(g),
		waiverListCmd(g),
		waiverApproveCmd(g),
		waiverRejectCmd(g),
		waiverWithdrawCmd(g),
	)

	return cmd
}

func waiverRequestCmd(g *globals) *cobra.Command {
	var (
		reason string
		output string
		rule   relicv1.IgnoreRule
	)

	cmd := &cobra.Command{
		Use:   "request",
		Short: "Ask administrators for an ignore rule",
		Example: "  relic security waiver request --package pkg:npm/lodash@4.17.15 --vulnerability CVE-2020-8203 \\\n" +
			"    --reason \"the vulnerable function is not reachable\"",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := g.dial()
			if err != nil {
				return err
			}

			res, err := c.waiver.RequestWaiver(cmd.Context(), &relicv1.RequestWaiverRequest{
				Rule:   &rule,
				Reason: reason,
			})
			if err != nil {
				return fmt.Errorf("request waiver: %w", err)
			}

			return printMessage(cmd.OutOrStdout(), output, res.GetWaiver(), func(w io.Writer) error {
				_, err := fmt.Fprintf(w, "requested waiver %s\n", res.GetWaiver().GetId())

				return err
			})
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "why the findings do not apply")
	cmd.Flags().
		StringSliceVar(&rule.Packages, "package", nil, "package URLs; one without a version matches every version")
	cmd.Flags().StringSliceVar(&rule.Vulnerabilities, "vulnerability", nil, "vulnerability IDs")
	cmd.Flags().StringSliceVar(&rule.Cves, "cve", nil, "CVE IDs")
	cmd.Flags().StringSliceVar(&rule.Licenses, "license", nil, "license IDs")
	cmd.Flags().StringSliceVar(&rule.ArtifactPaths, "artifact-path", nil, "<repository>/<path> prefixes")
	cmd.Flags().StringSliceVar(&rule.Watches, "watch", nil, "watches the rule is limited to")
	cmd.Flags().StringSliceVar(&rule.Policies, "policy", nil, "policies the rule is limited to")
	_ = cmd.MarkFlagRequired("reason")
	outputFlag(cmd, &output)

	return cmd
}

func waiverListCmd(g *globals) *cobra.Command {
	var (
		status   string
		mine     bool
		output   string
		page     int32
		pageSize int32
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List waivers, newest first: yours, or everyone's for administrators and viewers",
		Example: "  relic security waiver list --status pending\n  relic security waiver list --mine -o json\n" +
			"  relic security waiver list --page 2 --page-size 200",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, ok := waiverStatusNames[status]
			if !ok {
				return fmt.Errorf("unknown status %q: pending, approved or rejected", status)
			}

			if page < 1 || pageSize < 1 || pageSize > 200 {
				return fmt.Errorf(
					"--page %d --page-size %d: pages count from 1 and hold 1 to 200 waivers",
					page,
					pageSize,
				)
			}

			c, err := g.dial()
			if err != nil {
				return err
			}

			res, err := c.waiver.ListWaivers(cmd.Context(), &relicv1.ListWaiversRequest{
				Status:   st,
				Mine:     mine,
				Page:     page,
				PageSize: pageSize,
			})
			if err != nil {
				return fmt.Errorf("list waivers: %w", err)
			}

			if err := printMessage(cmd.OutOrStdout(), output, res, func(w io.Writer) error {
				return printWaivers(w, res.GetWaivers())
			}); err != nil {
				return err
			}

			if shown := int64(page-1)*int64(pageSize) + int64(len(res.GetWaivers())); shown < res.GetTotal() {
				_, err = fmt.Fprintf(
					cmd.ErrOrStderr(),
					"%d of %d waivers; --page %d lists more\n",
					shown,
					res.GetTotal(),
					page+1,
				)
			}

			return err
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "pending, approved or rejected")
	cmd.Flags().BoolVar(&mine, "mine", false, "only your waivers")
	cmd.Flags().Int32Var(&page, "page", 1, "the page to list, counting from 1")
	cmd.Flags().Int32Var(&pageSize, "page-size", 50, "waivers per page, at most 200")
	_ = cmd.RegisterFlagCompletionFunc(
		"status",
		cobra.FixedCompletions(
			[]string{
				"pending",
				"approved",
				"rejected",
			},
			cobra.ShellCompDirectiveNoFileComp,
		),
	)
	outputFlag(cmd, &output)

	return cmd
}

func printWaivers(w io.Writer, waivers []*relicv1.Waiver) error {
	table := terminal.Table{
		Headers: []string{
			"ID",
			"STATUS",
			"REQUESTED BY",
			"FILTERS",
			"EXPIRES",
			"REASON",
		},
	}

	for _, wv := range waivers {
		expires := ""
		if e := wv.GetExpires(); e != nil {
			expires = e.AsTime().Local().Format(time.DateOnly)
		}

		table.Rows = append(table.Rows, []string{
			wv.GetId(),
			strings.ToLower(strings.TrimPrefix(wv.GetStatus().String(), "WAIVER_STATUS_")),
			wv.GetRequestedBy(),
			tableCell(ruleFilters(wv.GetRule())),
			expires,
			tableCell(wv.GetReason()),
		})
	}

	return table.Print(w)
}

func ruleFilters(r *relicv1.IgnoreRule) string {
	var parts []string
	for _, f := range [][]string{
		r.GetPackages(),
		r.GetVulnerabilities(),
		r.GetCves(),
		r.GetLicenses(),
		r.GetArtifactPaths(),
	} {
		parts = append(parts, f...)
	}

	for _, w := range r.GetWatches() {
		parts = append(parts, "watch:"+w)
	}

	for _, p := range r.GetPolicies() {
		parts = append(parts, "policy:"+p)
	}

	for _, b := range r.GetBuilds() {
		parts = append(parts, "build:"+strings.TrimSuffix(b.GetName()+"/"+b.GetNumber(), "/"))
	}

	return strings.Join(parts, ",")
}

func waiverApproveCmd(g *globals) *cobra.Command {
	var (
		days int32
		note string
	)

	cmd := &cobra.Command{
		Use:     "approve <id>",
		Short:   "Approve a pending waiver: it becomes an ignore rule that expires",
		Example: "  relic security waiver approve 0192f7a0-... --days 30 --note \"until the upgrade\"",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.dial()
			if err != nil {
				return err
			}

			res, err := c.waiver.ApproveWaiver(cmd.Context(), &relicv1.ApproveWaiverRequest{
				Id:   args[0],
				Days: days,
				Note: note,
			})
			if err != nil {
				return fmt.Errorf("approve %s: %w", args[0], err)
			}

			_, err = fmt.Fprintf(
				cmd.OutOrStdout(),
				"approved %s: ignore rule %s expires %s\n",
				args[0],
				res.GetWaiver().GetIgnoreRuleId(),
				res.GetWaiver().GetExpires().AsTime().Local().Format(time.DateOnly),
			)

			return err
		},
	}
	cmd.Flags().Int32Var(&days, "days", 30, "days the ignore rule lasts, 1 to 365")
	cmd.Flags().StringVar(&note, "note", "", "a note for the requester")

	return cmd
}

func waiverRejectCmd(g *globals) *cobra.Command {
	var note string

	cmd := &cobra.Command{
		Use:     "reject <id>",
		Short:   "Reject a pending waiver",
		Example: "  relic security waiver reject 0192f7a0-... --note \"upgrade instead\"",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.dial()
			if err != nil {
				return err
			}

			if _, err := c.waiver.RejectWaiver(cmd.Context(), &relicv1.RejectWaiverRequest{
				Id:   args[0],
				Note: note,
			}); err != nil {
				return fmt.Errorf("reject %s: %w", args[0], err)
			}

			_, err = fmt.Fprintf(cmd.OutOrStdout(), "rejected %s\n", args[0])

			return err
		},
	}
	cmd.Flags().StringVar(&note, "note", "", "a note for the requester")

	return cmd
}

func waiverWithdrawCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:     "withdraw <id>",
		Short:   "Withdraw one of your pending waivers",
		Example: "  relic security waiver withdraw 0192f7a0-...",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.dial()
			if err != nil {
				return err
			}

			if _, err := c.waiver.WithdrawWaiver(cmd.Context(), &relicv1.WithdrawWaiverRequest{
				Id: args[0],
			}); err != nil {
				return fmt.Errorf("withdraw %s: %w", args[0], err)
			}

			_, err = fmt.Fprintf(cmd.OutOrStdout(), "withdrew %s\n", args[0])

			return err
		},
	}
}
