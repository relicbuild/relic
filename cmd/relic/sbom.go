package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"

	"github.com/spf13/cobra"
)

func sbomCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sbom",
		Short: "Export an item's SBOM as CycloneDX or SPDX, or import one",
	}
	cmd.AddCommand(sbomExportCmd(g), sbomImportCmd(g))

	return cmd
}

func sbomExportCmd(g *globals) *cobra.Command {
	var format, file string

	cmd := &cobra.Command{
		Use:   "export <repository>/<path>",
		Short: "Write an item's SBOM as CycloneDX, SPDX or Syft JSON",
		Long: "Writes the SBOM Relic Scan made for an item, or the one imported for it, to standard output or\n" +
			"--file. For a container image, give the image's folder (repository/image/tag).",
		Example: "  relic security sbom export libs-local/app/app-1.4.0.jar > app.cdx.json\n" +
			"  relic security sbom export docker-local/web/1.2 --format spdx --file web.spdx.json",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, p, err := splitTarget(args[0])
			if err != nil {
				return err
			}

			c, err := g.dial()
			if err != nil {
				return err
			}

			doc, err := c.exportSBOM(cmd.Context(), repo, p, format)
			if err != nil {
				return err
			}

			if file == "" {
				_, err = cmd.OutOrStdout().Write(doc)
				return err
			}

			return writeFileAtomic(file, bytes.NewReader(doc), 0o600)
		},
	}
	cmd.Flags().StringVar(&format, "format", "cyclonedx", "cyclonedx, spdx or syft")
	cmd.Flags().StringVar(&file, "file", "", "write the SBOM to this file instead of standard output")
	_ = cmd.RegisterFlagCompletionFunc(
		"format",
		cobra.FixedCompletions(
			[]string{
				"cyclonedx",
				"spdx",
				"syft",
			},
			cobra.ShellCompDirectiveNoFileComp,
		),
	)

	return cmd
}

func sbomImportCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "import <repository>/<path> <document>",
		Short: "Replace an item's SBOM with a CycloneDX, SPDX or Syft JSON document",
		Long: "Replaces the SBOM of an item's content with a document you made, for example in your build, and\n" +
			"has Relic Scan match it for vulnerabilities. Every file with the same content shares the SBOM, so\n" +
			"this needs deploy permission on each of them.",
		Example: "  relic security sbom import libs-local/app/app-1.4.0.jar build/bom.json",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, p, err := splitTarget(args[0])
			if err != nil {
				return err
			}

			doc, err := os.ReadFile(args[1])
			if err != nil {
				return err
			}

			c, err := g.dial()
			if err != nil {
				return err
			}

			if err := c.importSBOM(cmd.Context(), repo, p, doc); err != nil {
				return err
			}

			_, err = fmt.Fprintf(cmd.ErrOrStderr(), "imported the SBOM of %s/%s\n", repo, p)

			return err
		},
	}
}

func (c *client) exportSBOM(ctx context.Context, repo, p, format string) ([]byte, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		c.itemURL("/api/v1/sbom/", repo, p)+"?format="+url.QueryEscape(format),
		nil,
	)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("export the SBOM of %s/%s: %w", repo, p, httpError(resp))
	}

	return io.ReadAll(resp.Body)
}

func (c *client) importSBOM(ctx context.Context, repo, p string, doc []byte) error {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPut,
		c.itemURL("/api/v1/sbom/", repo, p),
		bytes.NewReader(doc),
	)
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

	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("import the SBOM of %s/%s: %w", repo, p, httpError(resp))
	}

	return nil
}
