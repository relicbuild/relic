package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestUnknownSubcommandsFail(t *testing.T) {
	t.Parallel()

	root := newRootCmd(func(string) string {
		return ""
	})

	var groups [][]string

	var walk func(c *cobra.Command, path []string)

	walk = func(c *cobra.Command, path []string) {
		if c.HasSubCommands() && len(path) > 0 && !strings.Contains(c.Use, " ") {
			groups = append(groups, path)
		}

		for _, sub := range c.Commands() {
			walk(sub, append(slices.Clone(path), sub.Name()))
		}
	}
	walk(root, nil)

	for _, group := range groups {
		t.Run(strings.Join(group, " "), func(t *testing.T) {
			t.Parallel()

			_, err := runRelic(t, nil, append(slices.Clone(group), "no-such-command")...)
			if err == nil || !strings.Contains(err.Error(), `unknown command "no-such-command"`) {
				t.Errorf("relic %s no-such-command = %v, want an unknown command error", strings.Join(group, " "), err)
			}
		})
	}
}

func TestUnknownSubcommandSuggestsTheNearestOne(t *testing.T) {
	t.Parallel()

	_, err := runRelic(t, nil, "build", "lst")
	if err == nil || !strings.Contains(err.Error(), `unknown command "lst" for "relic build"`) ||
		!strings.Contains(err.Error(), "list") {
		t.Errorf("relic build lst = %v", err)
	}

	if _, err := runRelic(t, nil, "build"); err != nil {
		t.Errorf("relic build = %v, want its help", err)
	}
}

func TestLoadSettingsSetsTheEnvironment(t *testing.T) {
	t.Setenv("RELIC_LOG_LEVEL", "info")
	t.Setenv("AWS_REGION", "")

	path := filepath.Join(t.TempDir(), "server.env")
	settings := "# Relic server settings\n\nRELIC_LOG_LEVEL=debug\nAWS_REGION=eu-central-1\n"

	if err := os.WriteFile(path, []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := loadSettings(path); err != nil {
		t.Fatal(err)
	}

	for key, want := range map[string]string{
		"RELIC_LOG_LEVEL": "debug",
		"AWS_REGION":      "eu-central-1",
	} {
		if got := os.Getenv(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}

	if err := loadSettings(filepath.Join(t.TempDir(), "missing.env")); err == nil {
		t.Error("a missing settings file loaded")
	}
}
