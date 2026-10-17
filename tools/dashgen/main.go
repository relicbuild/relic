// Command dashgen writes Relic's Grafana dashboards from their Go
// definitions into deploy/observability/grafana/dashboards, and copies the
// observability files the Helm chart ships into helm/relic/files.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	dashboardsDir = "deploy/observability/grafana/dashboards"
	rulesFile     = "deploy/observability/prometheus/relic-rules.yaml"
	chartFiles    = "helm/relic/files/observability"
)

func main() {
	files, err := generate()
	if err == nil {
		err = write(files)
	}

	if err == nil {
		err = removeOrphans(files)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "dashgen:", err)
		os.Exit(1)
	}
}

func generate() (map[string][]byte, error) {
	out := map[string][]byte{}

	for _, d := range dashboards() {
		var buf bytes.Buffer

		enc := json.NewEncoder(&buf)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)

		if err := enc.Encode(d); err != nil {
			return nil, fmt.Errorf("%s: %w", d.UID, err)
		}

		name := d.UID + ".json"
		out[filepath.Join(dashboardsDir, name)] = buf.Bytes()
		out[filepath.Join(chartFiles, "dashboards", name)] = buf.Bytes()
	}

	vendored, err := filepath.Glob(filepath.Join(dashboardsDir, "third-party", "*.json"))
	if err != nil {
		return nil, err
	}

	for _, p := range vendored {
		b, err := os.ReadFile(p) //nolint:gosec // a path in the repository
		if err != nil {
			return nil, err
		}

		out[filepath.Join(chartFiles, "dashboards", filepath.Base(p))] = b
	}

	rules, err := os.ReadFile(rulesFile)
	if err != nil {
		return nil, err
	}

	out[filepath.Join(chartFiles, "relic-rules.yaml")] = rules

	return out, nil
}

func orphans(files map[string][]byte) ([]string, error) {
	var out []string

	for _, dir := range []string{
		dashboardsDir,
		filepath.Join(chartFiles, "dashboards"),
	} {
		found, err := filepath.Glob(filepath.Join(dir, "*.json"))
		if err != nil {
			return nil, err
		}

		for _, p := range found {
			if _, ok := files[p]; !ok {
				out = append(out, p)
			}
		}
	}

	return out, nil
}

func removeOrphans(files map[string][]byte) error {
	stale, err := orphans(files)
	if err != nil {
		return err
	}

	for _, p := range stale {
		if err := os.Remove(p); err != nil {
			return err
		}
	}

	return nil
}

func write(files map[string][]byte) error {
	for p, b := range files {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil { //nolint:gosec // repository directories
			return err
		}

		if err := os.WriteFile(p, b, 0o644); err != nil { //nolint:gosec // public repository files
			return err
		}
	}

	return nil
}
