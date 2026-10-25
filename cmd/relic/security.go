package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	relicv1 "relic/api/gen/relic/v1"

	"relic/internal/platform/terminal"
)

type scanFlags struct {
	syft       string
	watches    []string
	repository string
	wait       time.Duration
	fail       bool
	output     string
}

func (f *scanFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.syft, "syft", "syft", "the Syft binary that catalogs the packages")
	cmd.Flags().StringArrayVar(&f.watches, "watch", nil, "apply this watch's policies (repeatable)")
	cmd.Flags().
		StringVar(&f.repository, "repository", "", "apply the policies of the watches covering this repository")
	cmd.Flags().DurationVar(&f.wait, "wait", 10*time.Minute, "how long to wait for the scan")
	cmd.Flags().BoolVar(&f.fail, "fail", true, "exit with status 1 when a violated rule fails builds")
	outputFlag(cmd, &f.output)
}

// syftMissingError reports that the Syft binary was not found.
type syftMissingError struct {
	path string
}

func (e syftMissingError) Error() string {
	return e.path + " is not installed: relic catalogs packages with Syft (https://github.com/anchore/syft); " +
		"install it or point --syft at it"
}

func localPath(target string) (string, bool) {
	for _, scheme := range []string{
		"dir:",
		"file:",
	} {
		if p, ok := strings.CutPrefix(target, scheme); ok {
			return p, true
		}
	}

	return target, strings.HasPrefix(target, "/") || strings.HasPrefix(target, ".")
}

func syftPURLs(ctx context.Context, syft, target string) ([]string, error) {
	if p, ok := localPath(target); ok {
		if _, err := os.Stat(p); err != nil {
			if pathErr, isPath := errors.AsType[*fs.PathError](err); isPath {
				return nil, pathErr.Err
			}

			return nil, err
		}
	}

	var stderr bytes.Buffer

	//nolint:gosec // G204: Syft, as the user names it, with the target as one argument; no shell
	cmd := exec.CommandContext(ctx, syft, "scan", target, "-o", "syft-json")
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
		return nil, syftMissingError{
			path: syft,
		}
	}

	if err != nil {
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return nil, fmt.Errorf("syft %s: %w: %s", target, err, detail)
		}

		return nil, fmt.Errorf("syft %s: %w", target, err)
	}

	return sbomPURLs(out)
}

func sbomPURLs(b []byte) ([]string, error) {
	var doc struct {
		BOMFormat   string            `json:"bomFormat"`
		Components  []json.RawMessage `json:"components"`
		SPDXVersion string            `json:"spdxVersion"`
		Packages    []struct {
			ExternalRefs []struct {
				ReferenceType    string `json:"referenceType"`
				ReferenceLocator string `json:"referenceLocator"`
			} `json:"externalRefs"`
		} `json:"packages"`
		Artifacts []struct {
			PURL string `json:"purl"`
		} `json:"artifacts"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("read the SBOM: %w", err)
	}

	var out []string

	switch {
	case doc.BOMFormat == "CycloneDX":
		var walk func([]json.RawMessage) error

		walk = func(cs []json.RawMessage) error {
			for _, raw := range cs {
				var c struct {
					PURL       string            `json:"purl"`
					Components []json.RawMessage `json:"components"`
				}
				if err := json.Unmarshal(raw, &c); err != nil {
					return err
				}

				out = append(out, c.PURL)

				if err := walk(c.Components); err != nil {
					return err
				}
			}

			return nil
		}

		if err := walk(doc.Components); err != nil {
			return nil, fmt.Errorf("read the SBOM: %w", err)
		}
	case doc.SPDXVersion != "":
		for _, p := range doc.Packages {
			for _, r := range p.ExternalRefs {
				if r.ReferenceType == "purl" {
					out = append(out, r.ReferenceLocator)
				}
			}
		}
	case doc.Artifacts != nil:
		for _, a := range doc.Artifacts {
			out = append(out, a.PURL)
		}
	default:
		return nil, errors.New("read the SBOM: not CycloneDX, SPDX or Syft JSON")
	}

	return slices.DeleteFunc(out, func(p string) bool {
		return p == ""
	}), nil
}

func scanPackages(
	cmd *cobra.Command,
	g *globals,
	purls []string,
	f *scanFlags,
) (*relicv1.GetPackagesScanResponse, error) {
	purls = slices.Compact(slices.Sorted(slices.Values(purls)))
	if len(purls) == 0 {
		return nil, errors.New("no packages found to scan")
	}

	c, err := g.dial()
	if err != nil {
		return nil, err
	}

	q, err := c.scans.ScanPackages(
		cmd.Context(),
		&relicv1.ScanPackagesRequest{
			Purls:      purls,
			Watches:    f.watches,
			Repository: f.repository,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), f.wait)
	defer cancel()

	delay := 250 * time.Millisecond

	for {
		r, err := c.scans.GetPackagesScan(
			ctx,
			&relicv1.GetPackagesScanRequest{
				Id: q.GetId(),
			},
		)
		if err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}

		switch r.GetScan().GetStatus() {
		case relicv1.ScanStatus_SCAN_STATUS_QUEUED:
		case relicv1.ScanStatus_SCAN_STATUS_FAILED:
			return nil, fmt.Errorf("scan failed: %s", r.GetScan().GetReason())
		default:
			return r, nil
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("scan: still queued after %s", f.wait)
		case <-time.After(delay):
			delay = min(2*delay, 5*time.Second)
		}
	}
}

func printScan(w io.Writer, title string, s *relicv1.ItemScan, violations []*relicv1.PolicyViolation) {
	_, _ = fmt.Fprintf(
		w,
		"%s: %d components, %d findings (critical %d, high %d, medium %d, low %d)\n",
		title,
		s.GetComponents(),
		len(s.GetFindings()),
		s.GetCounts()["critical"],
		s.GetCounts()["high"],
		s.GetCounts()["medium"],
		s.GetCounts()["low"],
	)

	findings := terminal.Table{
		Headers: []string{
			"SEVERITY",
			"ID",
			"PACKAGE",
			"FIXED IN",
			"REACHABILITY",
		},
	}

	for _, f := range s.GetFindings() {
		severity := strings.TrimPrefix(strings.ToLower(f.GetSeverity().String()), "severity_")
		if f.GetMalicious() {
			severity = "MALICIOUS"
		}

		reach := ""

		switch f.GetReachability() {
		case "reachable":
			reach = "reachable: " + f.GetReachabilityTrace()
		case "not_reachable":
			reach = "not reachable"
		}

		findings.Rows = append(findings.Rows, []string{
			severity,
			f.GetId(),
			f.GetPurl(),
			strings.Join(f.GetFixedVersions(), ", "),
			tableCell(reach),
		})
	}

	if len(findings.Rows) > 0 {
		_ = findings.Print(w)
	}

	violated := terminal.Table{
		Headers: []string{
			"VIOLATION",
			"PACKAGE",
			"POLICY",
			"RULE",
			"WATCH",
			"ACTIONS",
		},
	}

	for _, v := range violations {
		var actions []string
		if v.GetFailBuild() {
			actions = append(actions, "fails builds")
		}

		if v.GetBlockDownload() {
			actions = append(actions, "blocks downloads")
		}

		violated.Rows = append(violated.Rows, []string{
			v.GetFindingId(),
			v.GetPurl(),
			v.GetPolicy(),
			v.GetRule(),
			v.GetWatch(),
			strings.Join(actions, ", "),
		})
	}

	if len(violated.Rows) > 0 {
		_ = violated.Print(w)
	}
}

func scanCmd(g *globals) *cobra.Command {
	var (
		f    scanFlags
		sbom bool
	)

	cmd := &cobra.Command{
		Use:   "scan <file|directory|image>...",
		Short: "Scan files, directories or container images for vulnerable packages",
		Long: "Catalogs the packages in each target with Syft (archives, binaries, directories, and images " +
			"from a registry, the Docker daemon or an archive) and scans them with Relic Scan, applying the " +
			"policies of --watch and of the watches covering --repository. With --sbom the targets are SBOMs " +
			"(CycloneDX, SPDX or Syft JSON) and nothing is cataloged. Exits with status 1 when a violated rule " +
			"fails builds, unless --fail=false.",
		Example: "  relic security scan target/app.jar\n" +
			"  relic security scan docker:team/app:1.4.0 --watch prod\n" +
			"  relic security scan --sbom bom.cdx.json -o json",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var purls []string

			for _, t := range args {
				var (
					found []string
					err   error
				)

				if sbom {
					var b []byte
					if b, err = os.ReadFile(t); err == nil { //nolint:gosec // an SBOM the user names
						found, err = sbomPURLs(b)
					}
				} else {
					found, err = syftPURLs(cmd.Context(), f.syft, t)
					if errors.As(err, &syftMissingError{}) {
						err = fmt.Errorf("%w, or pass an SBOM with --sbom", err)
					}
				}

				if err != nil {
					return fmt.Errorf("%s: %w", t, err)
				}

				purls = append(purls, found...)
			}

			res, err := scanPackages(cmd, g, purls, &f)
			if err != nil {
				return err
			}

			if err := printMessage(cmd.OutOrStdout(), f.output, res, func(w io.Writer) error {
				printScan(w, strings.Join(args, " "), res.GetScan(), res.GetViolations())

				return nil
			}); err != nil {
				return err
			}

			if f.fail && res.GetFailBuild() {
				return exitCode1
			}

			return nil
		},
	}
	f.register(cmd)
	cmd.Flags().BoolVar(&sbom, "sbom", false, "the targets are SBOM files")

	return cmd
}

func auditCmd(g *globals) *cobra.Command {
	var (
		f        scanFlags
		curation bool
	)

	cmd := &cobra.Command{
		Use:   "audit [<directory>]",
		Short: "Audit a project's dependencies: vulnerabilities, and what Relic would block",
		Long: "Catalogs the dependencies a project declares (lock files and manifests of npm, Go, Maven, " +
			"Gradle, Python, NuGet, Cargo, Ruby, … found by Syft) and scans them with Relic Scan. " +
			"With --curation --repository, it reports the dependencies that downloading through that " +
			"repository would block, and exits with status 1 when there are any.",
		Example: "  relic security audit\n" +
			"  relic security audit services/api --watch backend\n" +
			"  relic security audit --curation --repository npm-remote",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}

			if curation && f.repository == "" {
				return errors.New("audit --curation: name the repository the dependencies come through (--repository)")
			}

			purls, err := syftPURLs(cmd.Context(), f.syft, "dir:"+dir)
			if err != nil {
				return fmt.Errorf("audit %s: %w", dir, err)
			}

			res, err := scanPackages(cmd, g, purls, &f)
			if err != nil {
				return err
			}

			if curation {
				res.Violations = slices.DeleteFunc(res.Violations, func(v *relicv1.PolicyViolation) bool {
					return !v.GetBlockDownload()
				})
			}

			if err := printMessage(cmd.OutOrStdout(), f.output, res, func(w io.Writer) error {
				printScan(w, dir, res.GetScan(), res.GetViolations())

				if curation {
					blocked := map[string]bool{}
					for _, v := range res.GetViolations() {
						blocked[v.GetPurl()] = true
					}

					_, _ = fmt.Fprintf(w, "%d packages would be blocked through %s\n", len(blocked), f.repository)
				}

				return nil
			}); err != nil {
				return err
			}

			if (curation && res.GetBlockDownload()) || (!curation && f.fail && res.GetFailBuild()) {
				return exitCode1
			}

			return nil
		},
	}
	f.register(cmd)
	cmd.Flags().
		BoolVar(&curation, "curation", false, "report the dependencies downloading through --repository would block")

	return cmd
}
