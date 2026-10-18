package main

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/platform/terminal"
)

var decisionKindNames = map[string]relicv1.PolicyDecisionKind{
	"":         relicv1.PolicyDecisionKind_POLICY_DECISION_KIND_UNSPECIFIED,
	"download": relicv1.PolicyDecisionKind_POLICY_DECISION_KIND_DOWNLOAD,
	"build":    relicv1.PolicyDecisionKind_POLICY_DECISION_KIND_BUILD,
	"release":  relicv1.PolicyDecisionKind_POLICY_DECISION_KIND_RELEASE,
}

func decisionsCmd(g *globals) *cobra.Command {
	var (
		kind   string
		limit  int32
		output string
	)

	cmd := &cobra.Command{
		Use:     "decisions",
		Short:   "List the policy decisions that blocked downloads, builds and promotions",
		Example: "  relic security decisions\n  relic security decisions --kind download -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			k, ok := decisionKindNames[kind]
			if !ok {
				return fmt.Errorf("unknown kind %q: download, build or release", kind)
			}

			c, err := g.dial()
			if err != nil {
				return err
			}

			res, err := c.policyDecisions.ListPolicyDecisions(
				cmd.Context(),
				&relicv1.ListPolicyDecisionsRequest{
					Kind:  k,
					Limit: limit,
				},
			)
			if err != nil {
				return fmt.Errorf("list decisions: %w", err)
			}

			return printMessage(cmd.OutOrStdout(), output, res, func(w io.Writer) error {
				table := terminal.Table{
					Headers: []string{
						"KIND",
						"SUBJECT",
						"POLICY",
						"RULE",
						"COUNT",
						"LAST",
						"REASON",
					},
					RightAligned: []int{
						4,
					},
				}

				for _, d := range res.GetDecisions() {
					table.Rows = append(table.Rows, []string{
						strings.ToLower(strings.TrimPrefix(d.GetKind().String(), "POLICY_DECISION_KIND_")),
						d.GetSubject(),
						d.GetPolicy(),
						d.GetRule(),
						strconv.FormatInt(int64(d.GetCount()), 10),
						d.GetLastSeen().AsTime().Local().Format(time.DateTime),
						tableCell(d.GetReason()),
					})
				}

				return table.Print(w)
			})
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "download, build or release")
	cmd.Flags().Int32Var(&limit, "limit", 500, "the most decisions to list")
	_ = cmd.RegisterFlagCompletionFunc(
		"kind",
		cobra.FixedCompletions(
			[]string{
				"download",
				"build",
				"release",
			},
			cobra.ShellCompDirectiveNoFileComp,
		),
	)
	outputFlag(cmd, &output)

	return cmd
}

func policyCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "policy",
		Short: "Preview policies before applying them",
	}
	cmd.AddCommand(policyPreviewCmd(g))

	return cmd
}

func policyPreviewCmd(g *globals) *cobra.Command {
	var (
		files        []string
		repositories []string
		limit        int32
		output       string
	)

	cmd := &cobra.Command{
		Use:   "preview -f <file>...",
		Short: "List the stored files the policies in YAML files would violate",
		Long: "Evaluates each relic.build/v1 Policy in the files against the findings Relic Scan has stored,\n" +
			"without applying it, and lists the files it would raise violations on.",
		Example: "  relic security policy preview -f policies/critical.yaml\n" +
			"  relic security policy preview -f policies.yaml -o json --repository libs-release",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := g.dial()
			if err != nil {
				return err
			}

			docs, err := readDocuments(files, cmd.InOrStdin())
			if err != nil {
				return err
			}

			out := &relicv1.PreviewPolicyResponse{}
			previewed := 0

			for _, d := range docs {
				p, ok := d.msg.(*relicv1.Policy)
				if !ok {
					continue
				}

				previewed++

				res, err := c.policyDecisions.PreviewPolicy(
					cmd.Context(),
					&relicv1.PreviewPolicyRequest{
						Policy:       p,
						Repositories: repositories,
						Limit:        limit,
					},
				)
				if err != nil {
					return fmt.Errorf("preview %s: %w", d, err)
				}

				out.Hits = append(out.Hits, res.GetHits()...)
				out.Truncated = out.Truncated || res.GetTruncated()
			}

			if previewed == 0 {
				return errors.New("the files hold no Policy")
			}

			return printMessage(cmd.OutOrStdout(), output, out, func(w io.Writer) error {
				return printPreview(w, out)
			})
		},
	}
	cmd.Flags().
		StringSliceVarP(&files, "filename", "f", nil, "YAML file with relic.build/v1 Policy resources; - reads standard input")
	cmd.Flags().StringSliceVar(&repositories, "repository", nil, "preview only these repositories")
	cmd.Flags().Int32Var(&limit, "limit", 1000, "the most violations to list per policy")
	_ = cmd.MarkFlagRequired("filename")
	outputFlag(cmd, &output)

	return cmd
}

func printPreview(w io.Writer, out *relicv1.PreviewPolicyResponse) error {
	table := terminal.Table{
		Headers: []string{
			"FILE",
			"RULE",
			"ACTIONS",
			"ISSUE",
		},
	}

	for _, h := range out.GetHits() {
		var actions []string
		if h.GetFailBuild() {
			actions = append(actions, "fail build")
		}

		if h.GetBlockDownload() {
			actions = append(actions, "block download")
		}

		table.Rows = append(table.Rows, []string{
			h.GetRepository() + "/" + h.GetPath(),
			h.GetRule(),
			strings.Join(actions, ", "),
			tableCell(h.GetIssue()),
		})
	}

	if err := table.Print(w); err != nil {
		return err
	}

	_, err := fmt.Fprintf(w, "%d violations", len(out.GetHits()))
	if err == nil && out.GetTruncated() {
		_, err = fmt.Fprint(w, " (more not listed)")
	}

	if err == nil {
		_, err = fmt.Fprintln(w)
	}

	return err
}
