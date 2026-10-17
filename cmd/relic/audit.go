package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/timestamppb"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/platform/terminal"
)

var auditOutcomes = map[string]relicv1.AuditOutcome{
	"success": relicv1.AuditOutcome_AUDIT_OUTCOME_SUCCESS,
	"denied":  relicv1.AuditOutcome_AUDIT_OUTCOME_DENIED,
	"failure": relicv1.AuditOutcome_AUDIT_OUTCOME_FAILURE,
}

func auditListCmd(g *globals) *cobra.Command {
	var (
		output  string
		outcome string
		since   time.Duration
	)

	req := &relicv1.ListAuditEventsRequest{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List audit events, newest first",
		Long: "Lists changes to administration settings, sign-ins, tokens and blocked downloads. " +
			"Viewers and administrators only.",
		Example: "  relic audit list --since 24h\n" +
			"  relic audit list --actor bob --outcome denied\n" +
			"  relic audit list --action PutPermission -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if outcome != "" {
				o, ok := auditOutcomes[outcome]
				if !ok {
					return fmt.Errorf("--outcome %q: want success, denied or failure", outcome)
				}

				req.Outcome = o
			}

			if since > 0 {
				req.Since = timestamppb.New(time.Now().Add(-since))
			}

			c, err := g.dial()
			if err != nil {
				return err
			}

			res, err := c.audit.ListAuditEvents(cmd.Context(), connect.NewRequest(req))
			if err != nil {
				return fmt.Errorf("list audit events: %w", err)
			}

			return printMessage(cmd.OutOrStdout(), output, res.Msg, func(w io.Writer) error {
				return printAuditEvents(w, res.Msg.GetEvents())
			})
		},
	}
	cmd.Flags().StringVar(&req.Actor, "actor", "", "only this user's events")
	cmd.Flags().StringVar(&req.Action, "action", "", "only events whose action contains this text")
	cmd.Flags().StringVar(&req.Target, "target", "", "only events whose target contains this text")
	cmd.Flags().StringVar(&outcome, "outcome", "", "only events with this outcome: success, denied or failure")
	cmd.Flags().DurationVar(&since, "since", 0, "only events in this last period, such as 24h")
	cmd.Flags().Int32Var(&req.PageSize, "page-size", 100, "events per page, at most 1000")
	cmd.Flags().Int32Var(&req.Page, "page", 1, "the page to list, counting from 1")
	_ = cmd.RegisterFlagCompletionFunc(
		"outcome",
		cobra.FixedCompletions(
			[]string{
				"success",
				"denied",
				"failure",
			},
			cobra.ShellCompDirectiveNoFileComp,
		),
	)
	outputFlag(cmd, &output)

	return cmd
}

func printAuditEvents(w io.Writer, events []*relicv1.AuditEvent) error {
	table := terminal.Table{
		Headers: []string{
			"TIME",
			"ACTOR",
			"ACTION",
			"TARGET",
			"OUTCOME",
			"CLIENT",
		},
	}

	for _, e := range events {
		table.Rows = append(table.Rows, []string{
			e.GetTime().AsTime().Local().Format(time.DateTime),
			e.GetActor(),
			e.GetAction(),
			tableCell(e.GetTarget()),
			strings.ToLower(strings.TrimPrefix(e.GetOutcome().String(), "AUDIT_OUTCOME_")),
			tableCell(e.GetClient()),
		})
	}

	return table.Print(w)
}
