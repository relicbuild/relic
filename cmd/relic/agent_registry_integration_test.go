//go:build integration

package main

import (
	"strings"
	"testing"
)

func TestRegistryCommands(t *testing.T) {
	t.Parallel()

	config, dir := adminCLI(t)

	if _, err := relic(t, config, `apiVersion: relic.build/v1
kind: Repository
metadata: {name: cli-mcp}
spec: {class: local, format: mcp}
`, "apply", "-f", "-"); err != nil {
		t.Fatal(err)
	}

	put(
		t,
		config,
		dir,
		"cli-mcp/notes/0.1.0/server.json",
		`{"name":"io.example/notes","version":"0.1.0","description":"Notes"}`,
	)

	out, err := relic(t, config, "", "registry", "list", "cli-mcp")
	if err != nil || !strings.Contains(out, "io.example/notes │ 0.1.0   │ no") {
		t.Errorf("list = %q %v", out, err)
	}

	if out, err := relic(t, config, "", "registry", "approve", "cli-mcp/notes/0.1.0/server.json"); err != nil ||
		out != "approved cli-mcp/notes/0.1.0/server.json\n" {
		t.Errorf("approve = %q %v", out, err)
	}

	out, err = relic(t, config, "", "registry", "list", "cli-mcp")
	if err != nil || !strings.Contains(out, "by admin") {
		t.Errorf("list after approval = %q %v", out, err)
	}

	if _, err := relic(t, config, "", "registry", "revoke", "cli-mcp/notes/0.1.0/server.json"); err != nil {
		t.Errorf("revoke: %v", err)
	}

	if _, err := relic(t, config, "", "registry", "revoke", "cli-mcp/notes/0.1.0/server.json"); err == nil {
		t.Error("revoking twice succeeded")
	}
}
