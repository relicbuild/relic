package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	relicv1 "relic/api/gen/relic/v1"

	"relic/internal/platform/terminal"
)

func reportCmd(g *globals) *cobra.Command {
	var (
		output string
		failOn string
	)

	cmd := &cobra.Command{
		Use: "report <repository>/<path>",
		Short: "Show what Relic Scan found in a stored file: vulnerabilities, malicious packages, secrets, " +
			"misconfigurations, operational risks",
		Long: "Prints the stored scan of a file (for an image folder, its manifest's). --fail-on exits with " +
			"status 1 when a finding, issue or operational risk is at least that severe (critical, high, " +
			"medium, low), and fails while the file's scan is queued or has failed, so a " +
			"pipeline can gate on what is already in Relic.",
		Example:           "  relic security report libs-local/org/app/1.0/app.jar\n  relic security report charts/demo-0.1.0.tgz --fail-on high -o json",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeTargets(g),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, p, err := splitTarget(args[0])
			if err != nil {
				return err
			}

			threshold := relicv1.Severity_SEVERITY_UNSPECIFIED

			if failOn != "" {
				v, ok := relicv1.Severity_value["SEVERITY_"+strings.ToUpper(failOn)]
				if !ok || v < int32(relicv1.Severity_SEVERITY_CRITICAL) || v > int32(relicv1.Severity_SEVERITY_LOW) {
					return fmt.Errorf("unknown severity %q: use critical, high, medium or low", failOn)
				}

				threshold = relicv1.Severity(v)
			}

			c, err := g.dial()
			if err != nil {
				return err
			}

			r, err := c.scans.GetItemScan(cmd.Context(), &relicv1.GetItemScanRequest{
				Repository: repo,
				Path:       p,
			})
			if err != nil {
				return fmt.Errorf("report: %w", err)
			}

			s := r.GetScan()

			if err := printMessage(cmd.OutOrStdout(), output, s, func(w io.Writer) error {
				status := strings.TrimPrefix(strings.ToLower(s.GetStatus().String()), "scan_status_")
				if s.GetReason() != "" {
					status += " (" + s.GetReason() + ")"
				}

				_, _ = fmt.Fprintf(w, "%s: %s\n", args[0], status)
				printScan(w, args[0], s, nil)

				issues := terminal.Table{
					Headers: []string{
						"SEVERITY",
						"ANALYZER",
						"RULE",
						"LOCATION",
						"ISSUE",
					},
				}

				for _, i := range s.GetIssues() {
					where := i.GetFile()
					if i.GetLine() > 0 {
						where += fmt.Sprintf(":%d", i.GetLine())
					}

					if i.GetResource() != "" {
						where += " (" + i.GetResource() + ")"
					}

					title := i.GetTitle()
					if v := i.GetValidation(); v != "" {
						title += " [" + v + "]"
					}

					issues.Rows = append(issues.Rows, []string{
						strings.TrimPrefix(strings.ToLower(i.GetSeverity().String()), "severity_"),
						i.GetAnalyzer(),
						i.GetRule(),
						where,
						tableCell(title),
					})
				}

				if len(issues.Rows) > 0 {
					if err := issues.Print(w); err != nil {
						return err
					}
				}

				risks := terminal.Table{
					Headers: []string{
						"SEVERITY",
						"KIND",
						"PACKAGE",
						"VERSION",
						"DETAIL",
					},
				}

				for _, r := range s.GetRisks() {
					risks.Rows = append(risks.Rows, []string{
						strings.TrimPrefix(strings.ToLower(r.GetSeverity().String()), "severity_"),
						r.GetKind(),
						r.GetPackageName(),
						r.GetPackageVersion(),
						tableCell(r.GetDetail()),
					})
				}

				if len(risks.Rows) > 0 {
					return risks.Print(w)
				}

				return nil
			}); err != nil {
				return err
			}

			if threshold == relicv1.Severity_SEVERITY_UNSPECIFIED {
				return nil
			}

			switch s.GetStatus() {
			case relicv1.ScanStatus_SCAN_STATUS_DONE,
				relicv1.ScanStatus_SCAN_STATUS_PARTIAL,
				relicv1.ScanStatus_SCAN_STATUS_NONE,
				relicv1.ScanStatus_SCAN_STATUS_SKIPPED:
			default:
				return fmt.Errorf(
					"--fail-on %s: %s is not scanned yet (%s)",
					failOn,
					args[0],
					strings.TrimPrefix(strings.ToLower(s.GetStatus().String()), "scan_status_"),
				)
			}

			if severeEnough(s, threshold) {
				return exitCode1
			}

			return nil
		},
	}
	outputFlag(cmd, &output)
	cmd.Flags().
		StringVar(&failOn, "fail-on", "", "exit with status 1 at this severity or worse: critical, high, medium or low")
	_ = cmd.RegisterFlagCompletionFunc(
		"fail-on",
		cobra.FixedCompletions(
			[]string{
				"critical",
				"high",
				"medium",
				"low",
			},
			cobra.ShellCompDirectiveNoFileComp,
		),
	)

	return cmd
}

// severeEnough reports whether a finding, an issue or an operational risk is
// at least as severe as threshold; severity enum values run from the most
// severe down.
func severeEnough(s *relicv1.ItemScan, threshold relicv1.Severity) bool {
	for _, f := range s.GetFindings() {
		if f.GetSeverity() != relicv1.Severity_SEVERITY_UNSPECIFIED && f.GetSeverity() <= threshold {
			return true
		}
	}

	for _, i := range s.GetIssues() {
		if i.GetSeverity() != relicv1.Severity_SEVERITY_UNSPECIFIED && i.GetSeverity() <= threshold {
			return true
		}
	}

	for _, r := range s.GetRisks() {
		if r.GetSeverity() != relicv1.Severity_SEVERITY_UNSPECIFIED && r.GetSeverity() <= threshold {
			return true
		}
	}

	return false
}
