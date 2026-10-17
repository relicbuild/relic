package scan

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// Analyzer names.
const (
	AnalyzerSecrets          = "secrets"
	AnalyzerMisconfiguration = "misconfiguration"
)

// Issue is something an analyzer found in scanned content: a secret or a
// misconfiguration. Only secret, which is never stored or returned, holds
// a secret, until the scan validates it.
type Issue struct {
	Analyzer string
	Rule     string
	Severity Severity
	Title    string
	File     string // relative to the content's root
	Line     int
	Resource string
	// Validation is a secret's: live, inactive, unknown or error; "" when
	// it was not checked.
	Validation string
	Validated  *time.Time

	secret string
}

// Analyzer finds issues in prepared content: source is what Syft
// catalogs (dir:<path>, file:<path> or oci-dir:<path>).
type Analyzer interface {
	// Name is the analyzer's (AnalyzerSecrets, AnalyzerMisconfiguration).
	Name() string
	Analyze(ctx context.Context, source string) ([]Issue, error)
}

// Gitleaks finds secrets.
type Gitleaks struct {
	Exec
	// Unredacted keeps each secret on its issue for the scan to validate.
	Unredacted bool
}

// Name implements Analyzer.
func (Gitleaks) Name() string { return AnalyzerSecrets }

// Analyze implements Analyzer.
func (g Gitleaks) Analyze(ctx context.Context, source string) ([]Issue, error) {
	kind, path, _ := strings.Cut(source, ":")
	if kind != "dir" && kind != "file" {
		return nil, nil
	}

	args := []string{
		"dir",
		path,
		"--max-archive-depth",
		"2",
		"--report-format",
		"json",
		"--report-path",
		"-",
		"--no-banner",
		"--log-level",
		"error",
		"--exit-code",
		"0",
	}
	if !g.Unredacted {
		args = append(args, "--redact")
	}

	out, err := g.run(ctx, args...)
	if err != nil {
		return nil, err
	}

	return parseGitleaks(out, path, g.Unredacted)
}

func parseGitleaks(out []byte, root string, keepSecrets bool) ([]Issue, error) {
	var findings []struct {
		RuleID      string `json:"RuleID"`
		Description string `json:"Description"`
		File        string `json:"File"`
		StartLine   int    `json:"StartLine"`
		Secret      string `json:"Secret"`
	}
	if err := json.Unmarshal(out, &findings); err != nil {
		return nil, fmt.Errorf("gitleaks: unexpected output: %w", err)
	}

	issues := make([]Issue, 0, len(findings))
	for _, f := range findings {
		is := Issue{
			Analyzer: AnalyzerSecrets,
			Rule:     f.RuleID,
			Severity: SeverityHigh,
			Title:    f.Description,
			File:     relative(root, f.File),
			Line:     f.StartLine,
		}
		if keepSecrets {
			is.secret = f.Secret
		}

		issues = append(issues, is)
	}

	return issues, nil
}

func relative(root, p string) string {
	if rel, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}

	return filepath.ToSlash(p)
}

// Trivy finds misconfigurations with its bundled checks.
type Trivy struct {
	Exec
	// CacheDir is Trivy's cache (checks, parsed state); empty uses a
	// temporary one per run.
	CacheDir string
}

// Name implements Analyzer.
func (Trivy) Name() string { return AnalyzerMisconfiguration }

// Analyze implements Analyzer: configuration files in directories, and an
// image's configuration.
func (t Trivy) Analyze(ctx context.Context, source string) ([]Issue, error) {
	kind, path, _ := strings.Cut(source, ":")

	common := []string{
		"--format",
		"json",
		"--quiet",
		"--skip-check-update",
		"--skip-version-check",
	}
	if t.CacheDir != "" {
		common = append(common, "--cache-dir", t.CacheDir)
	}

	var args []string

	switch kind {
	case "dir":
		args = append([]string{
			"config",
		}, common...)
		args = append(args, path)
	case "oci-dir":
		args = append([]string{
			"image",
			"--input",
			path,
			"--scanners",
			"",
			"--image-config-scanners",
			"misconfig,secret",
			"--skip-db-update",
		}, common...)
	default:
		return nil, nil
	}

	out, err := t.run(ctx, args...)
	if err != nil {
		return nil, err
	}

	return parseTrivy(out)
}

func parseTrivy(out []byte) ([]Issue, error) {
	var rep struct {
		Results []struct {
			Target            string `json:"Target"`
			Misconfigurations []struct {
				ID            string `json:"ID"`
				Title         string `json:"Title"`
				Message       string `json:"Message"`
				Severity      string `json:"Severity"`
				Status        string `json:"Status"`
				CauseMetadata struct {
					Resource  string `json:"Resource"`
					StartLine int    `json:"StartLine"`
				} `json:"CauseMetadata"`
			} `json:"Misconfigurations"`
			Secrets []struct {
				RuleID    string `json:"RuleID"`
				Title     string `json:"Title"`
				Severity  string `json:"Severity"`
				StartLine int    `json:"StartLine"`
			} `json:"Secrets"`
		} `json:"Results"`
	}
	if err := json.Unmarshal(out, &rep); err != nil {
		return nil, fmt.Errorf("trivy: unexpected output: %w", err)
	}

	var issues []Issue

	for _, r := range rep.Results {
		for _, m := range r.Misconfigurations {
			if m.Status != "" && m.Status != "FAIL" {
				continue
			}

			title := m.Title
			if m.Message != "" {
				title += ": " + m.Message
			}

			issues = append(issues, Issue{
				Analyzer: AnalyzerMisconfiguration,
				Rule:     m.ID,
				Severity: trivySeverity(m.Severity),
				Title:    title,
				File:     r.Target,
				Line:     m.CauseMetadata.StartLine,
				Resource: m.CauseMetadata.Resource,
			})
		}

		for _, s := range r.Secrets {
			issues = append(issues, Issue{
				Analyzer: AnalyzerSecrets,
				Rule:     s.RuleID,
				Severity: trivySeverity(s.Severity),
				Title:    s.Title,
				File:     r.Target,
				Line:     s.StartLine,
			})
		}
	}

	return issues, nil
}

func trivySeverity(s string) Severity {
	if v := Severity(strings.ToLower(s)); v.Known() {
		return v
	}

	return SeverityUnknown
}

// AnalyzerLicense finds licenses in files (ScanCode), beyond what packages
// declare.
const AnalyzerLicense = "license"

// ScanCode finds licenses in every file's text (ScanCode toolkit).
type ScanCode struct { //nolint:revive // the tool's name, like Grype and Syft
	Exec
}

// Name implements Analyzer.
func (ScanCode) Name() string { return AnalyzerLicense }

// Analyze implements Analyzer: directories and files; not images.
func (s ScanCode) Analyze(ctx context.Context, source string) ([]Issue, error) {
	kind, path, _ := strings.Cut(source, ":")
	if kind != "dir" && kind != "file" {
		return nil, nil
	}

	out, err := s.run(ctx, "--license", "--quiet", "--processes", "2", "--timeout", "120", "--json-pp", "-", path)
	if err != nil {
		return nil, err
	}

	return parseScanCode(out, filepath.Base(path))
}

func parseScanCode(out []byte, root string) ([]Issue, error) {
	var rep struct {
		Files []struct {
			Path       string `json:"path"`
			Type       string `json:"type"`
			Detections []struct {
				Expression string `json:"license_expression_spdx"`
				Matches    []struct {
					StartLine int `json:"start_line"`
				} `json:"matches"`
			} `json:"license_detections"`
		} `json:"files"`
	}
	if err := json.Unmarshal(out, &rep); err != nil {
		return nil, fmt.Errorf("scancode: unexpected output: %w", err)
	}

	var issues []Issue

	for _, f := range rep.Files {
		seen := map[string]bool{}

		for _, d := range f.Detections {
			if d.Expression == "" || seen[d.Expression] {
				continue
			}

			seen[d.Expression] = true
			line := 0

			if len(d.Matches) > 0 {
				line = d.Matches[0].StartLine
			}

			issues = append(issues, Issue{
				Analyzer: AnalyzerLicense,
				Rule:     d.Expression,
				Severity: SeverityUnknown,
				Title:    "License " + d.Expression,
				File:     strings.TrimPrefix(f.Path, root+"/"),
				Line:     line,
			})
		}
	}

	return issues, nil
}
