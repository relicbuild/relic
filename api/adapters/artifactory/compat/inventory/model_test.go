package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClassify(t *testing.T) {
	t.Parallel()

	yes, no := true, false
	rules := []Rule{
		{
			Match: []string{
				"rt build-collect-env",
			},
			LocalOnly: &yes,
		},
		{
			Match: []string{
				"*-config",
			},
			LocalOnly: &yes,
		},
		{
			Match: []string{
				"rt *",
			},
			Tier:      "A",
			Category:  "artifactory",
			LocalOnly: &no,
		},
		{
			Match: []string{
				"*",
			},
			Category: "other",
		},
	}

	tests := []struct {
		path      string
		wantTier  string
		wantCat   string
		wantLocal bool
	}{
		{
			"rt upload",
			"A",
			"artifactory",
			false,
		},
		{
			"rt build-collect-env",
			"A",
			"artifactory",
			true,
		},
		{
			"rt",
			"A",
			"artifactory",
			false,
		},
		{
			"rtx",
			"unassigned",
			"other",
			false,
		},
		{
			"npm-config",
			"unassigned",
			"other",
			true,
		},
		{
			"rt transfer-config",
			"A",
			"artifactory",
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()

			c := Command{
				Command: tt.path,
			}
			classify(&c, rules)

			if c.Tier != tt.wantTier || c.Category != tt.wantCat || c.LocalOnly != tt.wantLocal {
				t.Errorf(
					"got %s/%s/%v, want %s/%s/%v",
					c.Tier,
					c.Category,
					c.LocalOnly,
					tt.wantTier,
					tt.wantCat,
					tt.wantLocal,
				)
			}
		})
	}
}

func TestReadInventoryReportsMissingFile(t *testing.T) {
	t.Parallel()

	if _, err := readInventory(filepath.Join(t.TempDir(), "commands.yaml")); err == nil {
		t.Error("a missing inventory read as an empty one")
	}
}

func TestCoveredCommands(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()

		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("required.txt", "# comment\nping\n\n")
	write("ping.sh", "#!/bin/sh\n# covers: rt ping, rt curl\n")
	write("other.sh", "# covers: rt upload\n")

	got, err := coveredCommands(filepath.Join(dir, "required.txt"), dir)
	if err != nil {
		t.Fatal(err)
	}

	if !got["rt ping"] || !got["rt curl"] || got["rt upload"] || len(got) != 2 {
		t.Errorf("covered = %v", got)
	}

	write("required.txt", "ping\nsdk-artifactory-rest\n")

	got, err = coveredCommands(filepath.Join(dir, "required.txt"), dir)
	if err != nil || len(got) != 2 {
		t.Errorf("with the SDK suite: covered = %v, %v", got, err)
	}

	write("required.txt", "missing\n")

	if _, err := coveredCommands(filepath.Join(dir, "required.txt"), dir); err == nil {
		t.Error("missing required scenario not reported")
	}
}
