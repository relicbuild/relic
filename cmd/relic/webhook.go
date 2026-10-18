package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/platform/terminal"
)

func webhookDeliveriesCmd(g *globals) *cobra.Command {
	var (
		limit  int32
		output string
	)

	cmd := &cobra.Command{
		Use:     "deliveries [webhook]",
		Short:   "List the latest webhook deliveries, newest first",
		Example: "  relic webhook deliveries\n  relic webhook deliveries ci-notify -o json",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.dial()
			if err != nil {
				return err
			}

			name := ""
			if len(args) == 1 {
				name = args[0]
			}

			res, err := c.webhooks.ListWebhookDeliveries(
				cmd.Context(),
				&relicv1.ListWebhookDeliveriesRequest{
					Webhook: name,
					Limit:   limit,
				},
			)
			if err != nil {
				return fmt.Errorf("list deliveries: %w", err)
			}

			return printMessage(cmd.OutOrStdout(), output, res, func(w io.Writer) error {
				table := terminal.Table{
					Headers: []string{
						"ID",
						"WEBHOOK",
						"EVENT",
						"STATUS",
						"ATTEMPTS",
						"CODE",
						"CREATED",
						"ERROR",
					},
					RightAligned: []int{
						4,
						5,
					},
				}

				for _, d := range res.GetDeliveries() {
					table.Rows = append(table.Rows, []string{
						d.GetId(),
						d.GetWebhook(),
						d.GetEvent(),
						strings.ToLower(strings.TrimPrefix(d.GetStatus().String(), "WEBHOOK_DELIVERY_STATUS_")),
						strconv.Itoa(int(d.GetAttempts())),
						strconv.Itoa(int(d.GetResponseStatus())),
						d.GetCreated().AsTime().Local().Format(time.DateTime),
						tableCell(d.GetError()),
					})
				}

				return table.Print(w)
			})
		},
	}
	cmd.Flags().Int32Var(&limit, "limit", 100, "the most deliveries to list")
	outputFlag(cmd, &output)

	return cmd
}

func webhookPingCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:     "ping <webhook>",
		Short:   "Queue a webhook.ping event for a webhook",
		Example: "  relic webhook ping ci-notify",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.dial()
			if err != nil {
				return err
			}

			res, err := c.webhooks.PingWebhook(cmd.Context(), &relicv1.PingWebhookRequest{
				Name: args[0],
			})
			if err != nil {
				return fmt.Errorf("ping %s: %w", args[0], err)
			}

			_, err = fmt.Fprintf(cmd.OutOrStdout(), "queued delivery %s\n", res.GetDelivery())

			return err
		},
	}
}
