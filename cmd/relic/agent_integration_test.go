//go:build integration

package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackageSetupForAgents(t *testing.T) {
	t.Parallel()

	config, dir := adminCLI(t)
	home := filepath.Join(dir, "home")
	project := filepath.Join(dir, "project")

	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}

	if _, err := relic(t, config, `apiVersion: relic.build/v1
kind: Repository
metadata: {name: agent-npm}
spec: {class: local, format: npm}
`, "apply", "-f", "-"); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(project, "AGENTS.md"), []byte("# Project\n\nUse tabs.\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(
		filepath.Join(project, ".mcp.json"),
		[]byte(`{"mcpServers": {"docs": {"command": "docs-server"}}}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if _, err := relic(
			t,
			config,
			"",
			"package",
			"setup",
			"npm",
			"agent-npm",
			"--agent",
			"--project",
			project,
			"--home",
			home,
		); err != nil {
			t.Fatal(err)
		}
	}

	agents, _ := os.ReadFile(filepath.Join(project, "AGENTS.md"))
	if !strings.HasPrefix(string(agents), "# Project\n\nUse tabs.\n\n<!-- relic agent begin -->\n") ||
		strings.Count(string(agents), "<!-- relic agent begin -->") != 1 ||
		!strings.Contains(string(agents), "relic package check <package-url> --repository agent-npm") {
		t.Errorf("AGENTS.md = %q", agents)
	}

	var doc struct {
		Servers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}

	b, _ := os.ReadFile(filepath.Join(project, ".mcp.json"))
	if err := json.Unmarshal(b, &doc); err != nil || doc.Servers["docs"].Command != "docs-server" ||
		doc.Servers["relic"].Command != "relic" || doc.Servers["relic"].Args[0] != "mcp" {
		t.Errorf(".mcp.json = %s %v", b, err)
	}

	if npmrc, err := os.ReadFile(
		filepath.Join(home, ".npmrc"),
	); err != nil ||
		!strings.Contains(string(npmrc), "_authToken=") {
		t.Errorf("--agent did not write the client's configuration: %q %v", npmrc, err)
	}

	if strings.Contains(string(agents), "_authToken") || strings.Contains(strings.ToLower(string(b)), "token") {
		t.Error("a token landed in the project")
	}

	if err := os.WriteFile(filepath.Join(project, ".mcp.json"), []byte(`{"mcpServers": []}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := relic(
		t,
		config,
		"",
		"package",
		"setup",
		"npm",
		"agent-npm",
		"--agent",
		"--project",
		project,
		"--home",
		home,
	); err == nil || !strings.Contains(err.Error(), "mcpServers is not an object") {
		t.Errorf("setup over a .mcp.json whose mcpServers is a list = %v", err)
	}

	if after, err := os.ReadFile(filepath.Join(project, "AGENTS.md")); err != nil || string(after) != string(agents) {
		t.Errorf("the failed setup changed AGENTS.md: %q %v", after, err)
	}

	out, err := relic(t, config, "", "package", "check", "pkg:npm/left-pad@1.3.0", "--repository", "agent-npm")
	if ec, ok := errors.AsType[exitCode](err); !ok || ec != 1 || !strings.Contains(out, "is refused:") {
		t.Errorf("check of a version the repository lacks = %q %v", out, err)
	}

	if out, err := relic(t, config, "", "package", "check", "pkg:npm/left-pad@1.3.0"); err != nil ||
		out != "pkg:npm/left-pad@1.3.0 is allowed\n" {
		t.Errorf("check without a repository = %q %v", out, err)
	}
}
