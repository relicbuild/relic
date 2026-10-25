package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	relicv1 "relic/api/gen/relic/v1"
)

func vulnerabilityDatabaseCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "database",
		Short: "Import the vulnerability database Relic Scan matches with, and show its age",
		Long: "Relic Scan's worker downloads the vulnerability database on its own, unless " +
			"RELIC_SCAN_VULNERABILITY_DATABASE_URL is off: an installation without Internet access imports one " +
			"(Grype's database archive, which https://grype.anchore.io/databases/v6/latest.json names).",
	}
	cmd.AddCommand(
		vulnerabilityDatabaseImportCmd(g),
		vulnerabilityDatabaseStatusCmd(g),
	)

	return cmd
}

func vulnerabilityDatabaseImportCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "import <archive>",
		Short: "Upload a vulnerability database archive for the worker to install (administrators)",
		Example: "  # on a connected machine: the latest archive\n" +
			"  base=https://grype.anchore.io/databases/v6\n" +
			"  curl -fsSLo grype-db.tar.zst \"$base/$(curl -fsS $base/latest.json | jq -r .path)\"\n" +
			"  # inside\n  relic security database import grype-db.tar.zst",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := os.Open(args[0])
			if err != nil {
				return err
			}

			defer func() {
				_ = f.Close()
			}()

			st, err := f.Stat()
			if err != nil {
				return err
			}

			c, err := g.dial()
			if err != nil {
				return err
			}

			req, err := http.NewRequestWithContext(
				cmd.Context(),
				http.MethodPut,
				c.url+"/api/v1/vulnerability-database",
				f,
			)
			if err != nil {
				return err
			}

			req.ContentLength = st.Size()
			req.Header.Set("Content-Type", "application/octet-stream")

			resp, err := c.http.Do(req)
			if err != nil {
				return err
			}

			defer func() {
				_ = resp.Body.Close()
			}()

			if resp.StatusCode != http.StatusAccepted {
				return fmt.Errorf("vulnerability-database import: %w", httpError(resp))
			}

			_, _ = fmt.Fprintf(
				cmd.OutOrStdout(),
				"%s uploaded; the worker installs it and re-matches what was scanned (relic security database status)\n",
				args[0],
			)

			return nil
		},
	}
}

const scanOffLine = "Relic Scan is off: no worker with Syft and Grype reported lately"

// databaseBuiltLine says when the vulnerability database was built, in loc
// like the other security commands' times.
func databaseBuiltLine(built, now time.Time, loc *time.Location) string {
	return fmt.Sprintf("built %s (%s ago)", built.In(loc).Format(time.DateTime), now.Sub(built).Round(time.Hour))
}

func vulnerabilityDatabaseStatusCmd(g *globals) *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show when the vulnerability database scans use was built (viewers and administrators)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := g.dial()
			if err != nil {
				return err
			}

			r, err := c.system.GetSystemStatus(cmd.Context(), &relicv1.GetSystemStatusRequest{})
			if err != nil {
				return fmt.Errorf("vulnerability-database status: %w", err)
			}

			s := r.GetScanner()

			return printMessage(cmd.OutOrStdout(), output, s, func(w io.Writer) error {
				_, err := io.WriteString(w, databaseStatusText(s, time.Now(), time.Local))

				return err
			})
		},
	}
	outputFlag(cmd, &output)

	return cmd
}

func databaseStatusText(s *relicv1.ScannerStatus, now time.Time, loc *time.Location) string {
	var b strings.Builder

	if !s.GetAvailable() {
		b.WriteString(scanOffLine + "\n")
	}

	if s.GetVulnerabilityDatabaseBuilt() == nil {
		b.WriteString("no scan has used a vulnerability database yet\n")
	} else {
		b.WriteString(databaseBuiltLine(s.GetVulnerabilityDatabaseBuilt().AsTime(), now, loc) + "\n")
	}

	if failure := s.GetVulnerabilityDatabaseImportError(); failure != "" {
		fmt.Fprintf(
			&b,
			"the latest import failed at %s: %s\n",
			s.GetVulnerabilityDatabaseImportFailed().AsTime().In(loc).Format(time.DateTime),
			failure,
		)
	}

	return b.String()
}
