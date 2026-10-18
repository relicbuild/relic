package main

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	relicv1 "relic/api/gen/relic/v1"
)

func TestWritePackageCheck(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		res  *relicv1.CheckPackageResponse
		want []string
	}{
		{
			name: "allowed with a risk",
			res: &relicv1.CheckPackageResponse{
				Allowed: true,
				Risks: []*relicv1.OperationalRisk{
					{
						Detail: "Django 4.2 reached end of life on 2026-04-30",
					},
				},
			},
			want: []string{
				"pkg:pypi/django@4.2.11 is allowed\n",
				"│ risk │ Django 4.2 reached end of life on 2026-04-30 │",
			},
		},
		{
			name: "refused",
			res: &relicv1.CheckPackageResponse{
				Refusals: []*relicv1.PackageRefusal{
					{
						Reason: "MAL-1: steals tokens",
					},
				},
			},
			want: []string{
				"pkg:pypi/django@4.2.11 is refused:\n",
				"│ refusal │ MAL-1: steals tokens │",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			if err := writePackageCheck(&buf, "pkg:pypi/django@4.2.11", tt.res); err != nil {
				t.Fatal(err)
			}

			for _, want := range tt.want {
				if !strings.Contains(buf.String(), want) {
					t.Errorf("output = %q, want it to hold %q", buf.String(), want)
				}
			}
		})
	}
}

func TestMarkedBlocks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		old       string
		want      string
		wantSetup string
		wantErr   bool
	}{
		{
			name:      "appends to a file without a block",
			old:       "# Project\n",
			want:      "# Project\n\n<!-- relic agent begin -->\nnew\n<!-- relic agent end -->\n",
			wantSetup: "# Project\n# relic setup r begin\nnew\n# relic setup r end\n",
		},
		{
			name:      "replaces the block",
			old:       "# Project\n\n<!-- relic agent begin -->\nold\n<!-- relic agent end -->\nmine\n",
			want:      "# Project\n\n<!-- relic agent begin -->\nnew\n<!-- relic agent end -->\nmine\n",
			wantSetup: "# Project\n\n# relic setup r begin\nnew\n# relic setup r end\nmine\n",
		},
		{
			name:      "replaces the block in a file with CRLF line endings",
			old:       "# Project\r\n\r\n<!-- relic agent begin -->\r\nold\r\n<!-- relic agent end -->\r\nmine\r\n",
			want:      "# Project\r\n\r\n<!-- relic agent begin -->\nnew\n<!-- relic agent end -->\nmine\r\n",
			wantSetup: "# Project\r\n\r\n# relic setup r begin\nnew\n# relic setup r end\nmine\r\n",
		},
		{
			name:    "refuses a begin marker without its end marker",
			old:     "# Project\n<!-- relic agent begin -->\nmine\n",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := withAgentBlock(tt.old, "new\n")
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Errorf("withAgentBlock = %q, %v; want %q, error %v", got, err, tt.want, tt.wantErr)
			}

			setupOld := strings.ReplaceAll(tt.old, "<!-- relic agent begin -->", "# relic setup r begin")
			setupOld = strings.ReplaceAll(setupOld, "<!-- relic agent end -->", "# relic setup r end")

			if got, err := withBlock(setupOld, "r", "new\n"); (err != nil) != tt.wantErr || got != tt.wantSetup {
				t.Errorf("withBlock = %q, %v; want %q, error %v", got, err, tt.wantSetup, tt.wantErr)
			}
		})
	}
}

func TestWriteAgentFilesChangesNothingWhenTheMCPFileIsUnusable(t *testing.T) {
	t.Parallel()

	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, ".mcp.json"), []byte(`{"mcpServers": []}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := writeAgentFiles(io.Discard, project, "https://relic.example.com", "npm", "npm"); err == nil {
		t.Fatal("writeAgentFiles accepted an mcpServers list")
	}

	if _, err := os.Stat(filepath.Join(project, "AGENTS.md")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("AGENTS.md after a refused .mcp.json: %v, want it not written", err)
	}
}

func TestAgentInstructionsGiveTheClientsPackageURL(t *testing.T) {
	t.Parallel()

	for client, want := range map[string]string{
		"npm":       "pkg:npm/",
		"maven":     "pkg:maven/",
		"go":        "pkg:golang/",
		"pip":       "pkg:pypi/",
		"nuget":     "pkg:nuget/",
		"terraform": "<package-url>",
	} {
		got := agentInstructions("https://relic.example.com", client, "repo")
		if !strings.Contains(got, want) {
			t.Errorf("%s instructions = %q, want %s", client, got, want)
		}

		if client != "npm" && strings.Contains(got, "pkg:npm/") {
			t.Errorf("%s instructions give an npm package URL: %q", client, got)
		}
	}

	for name, client := range setupClients {
		if client.packageURL != "" && !strings.HasPrefix(client.packageURL, "pkg:") {
			t.Errorf("%s package URL example = %q", name, client.packageURL)
		}
	}
}
