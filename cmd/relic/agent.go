package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	relicv1 "relic/api/gen/relic/v1"

	"relic/internal/platform/terminal"
)

const (
	agentBlockBegin = "<!-- relic agent begin -->"
	agentBlockEnd   = "<!-- relic agent end -->"
)

// agentInstructions is the AGENTS.md section that routes a coding agent's
// dependencies through repo.
func agentInstructions(url, client, repo string) string {
	return fmt.Sprintf(`## Dependencies

Resolve every dependency through Relic at %[1]s, repository %[3]s: the %[2]s
configuration on this machine points there. Do not add another registry or
download packages from anywhere else.

Before adding or upgrading a dependency, ask Relic's MCP tool
package_allowed with its package URL (for example pkg:npm/lodash@4.17.21)
and repository %[3]s, or run:

    relic package check <package-url> --repository %[3]s

Never use a version it refuses; choose one it allows.
`, url, client, repo)
}

func withAgentBlock(old, content string) (string, error) {
	block := agentBlockBegin + "\n" + strings.TrimSuffix(content, "\n") + "\n" + agentBlockEnd + "\n"

	if i, _ := markerLine(old, agentBlockBegin); i >= 0 {
		_, end := markerLine(old[i:], agentBlockEnd)
		if end < 0 {
			return "", fmt.Errorf("%q has no matching %q; fix the file by hand", agentBlockBegin, agentBlockEnd)
		}

		return old[:i] + block + old[i+end:], nil
	}

	if old != "" && !strings.HasSuffix(old, "\n") {
		old += "\n"
	}

	if old != "" {
		old += "\n"
	}

	return old + block, nil
}

func markerLine(s, marker string) (start, end int) {
	for _, ending := range []string{
		"\n",
		"\r\n",
	} {
		if i := strings.Index(s, marker+ending); i >= 0 {
			return i, i + len(marker) + len(ending)
		}
	}

	return -1, -1
}

// writeAgentFiles writes the AGENTS.md section and registers relic mcp in
// the project's .mcp.json, keeping what else they hold.
func writeAgentFiles(w io.Writer, project, url, client, repo string) error {
	agents := filepath.Join(project, "AGENTS.md")

	old, err := os.ReadFile(agents) //nolint:gosec // the project the user names
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	content, err := withAgentBlock(string(old), agentInstructions(url, client, repo))
	if err != nil {
		return fmt.Errorf("%s: %w", agents, err)
	}

	mcpFile := filepath.Join(project, ".mcp.json")
	doc := map[string]any{}

	if b, err := os.ReadFile(mcpFile); err == nil { //nolint:gosec // the project the user names
		if err := json.Unmarshal(b, &doc); err != nil {
			return fmt.Errorf("%s: %w", mcpFile, err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	servers := map[string]any{}

	if v, ok := doc["mcpServers"]; ok && v != nil {
		servers, ok = v.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: mcpServers is not an object", mcpFile)
		}
	}

	servers["relic"] = map[string]any{
		"command": "relic",
		"args": []string{
			"mcp",
		},
	}
	doc["mcpServers"] = servers

	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}

	if err := writeFileAtomic(agents, strings.NewReader(content), 0o644); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(w, "wrote %s\n", agents)

	if err := writeFileAtomic(mcpFile, bytes.NewReader(append(b, '\n')), 0o644); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(w, "wrote %s\n", mcpFile)

	return nil
}

// writePackageCheck writes a package check's verdict, its refusals and the
// version's operational risks.
func writePackageCheck(w io.Writer, purl string, res *relicv1.CheckPackageResponse) error {
	verdict := "allowed"
	if !res.GetAllowed() {
		verdict = "refused:"
	}

	if _, err := fmt.Fprintf(w, "%s is %s\n", purl, verdict); err != nil {
		return err
	}

	table := terminal.Table{
		Headers: []string{
			"KIND",
			"DETAIL",
		},
	}

	for _, r := range res.GetRefusals() {
		table.Rows = append(table.Rows, []string{
			"refusal",
			tableCell(r.GetReason()),
		})
	}

	for _, r := range res.GetRisks() {
		table.Rows = append(table.Rows, []string{
			"risk",
			tableCell(r.GetDetail()),
		})
	}

	if len(table.Rows) == 0 {
		return nil
	}

	return table.Print(w)
}

func packageCheckCmd(g *globals) *cobra.Command {
	var (
		repo   string
		output string
	)

	cmd := &cobra.Command{
		Use:   "check <package-url>",
		Short: "Check whether a package version may be used; exits 1 when it is refused",
		Example: "  relic package check pkg:npm/lodash@4.17.21 --repository npm-virtual\n" +
			"  relic package check pkg:pypi/requests@2.32.3 -o json",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.dial()
			if err != nil {
				return err
			}

			res, err := c.scans.CheckPackage(cmd.Context(), connect.NewRequest(&relicv1.CheckPackageRequest{
				Purl:       args[0],
				Repository: repo,
			}))
			if err != nil {
				return fmt.Errorf("check %s: %w", args[0], err)
			}

			if err := printMessage(cmd.OutOrStdout(), output, res.Msg, func(w io.Writer) error {
				return writePackageCheck(w, args[0], res.Msg)
			}); err != nil {
				return err
			}

			if !res.Msg.GetAllowed() {
				return exitCode(1)
			}

			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repository", "", "the repository the package would come from")
	outputFlag(cmd, &output)

	return cmd
}
