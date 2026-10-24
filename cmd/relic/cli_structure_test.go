package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func runRelic(t *testing.T, env map[string]string, args ...string) (string, error) {
	t.Helper()

	var out bytes.Buffer

	root := newRootCmd(func(k string) string {
		return env[k]
	})
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs(args)

	err := root.Execute()

	return out.String(), err
}

func TestExamplesNameExistingCommands(t *testing.T) {
	t.Parallel()

	root := newRootCmd(func(string) string {
		return ""
	})

	var walk func(c *cobra.Command)

	walk = func(c *cobra.Command) {
		for line := range strings.SplitSeq(c.Example, "\n") {
			words := strings.Fields(line)
			if len(words) < 2 || words[0] != "relic" {
				continue
			}

			found, _, err := root.Find(words[1:])
			if err != nil || found == root {
				t.Errorf("%s example %q names no command: %v", c.CommandPath(), strings.TrimSpace(line), err)
			}
		}

		for _, sub := range c.Commands() {
			walk(sub)
		}
	}

	walk(root)
}

func TestRootListsGroups(t *testing.T) {
	t.Parallel()

	root := newRootCmd(func(string) string {
		return ""
	})

	names := make([]string, 0, len(root.Commands()))
	for _, c := range root.Commands() {
		names = append(names, c.Name())
	}

	for _, gone := range []string{
		"cp",
		"upload",
		"login",
		"setup",
		"status",
		"scan",
		"worker",
		"vulnerability-database",
	} {
		if strings.Contains(" "+strings.Join(names, " ")+" ", " "+gone+" ") {
			t.Errorf("%s is still a root command: %v", gone, names)
		}
	}

	for _, path := range [][]string{
		{
			"server",
			"start",
		},
		{
			"server",
			"setup",
		},
		{
			"server",
			"config",
			"set",
		},
		{
			"auth",
			"login",
		},
		{
			"auth",
			"logout",
		},
		{
			"auth",
			"context",
			"use",
		},
		{
			"artifact",
			"cp",
		},
		{
			"security",
			"database",
			"import",
		},
		{
			"package",
			"setup",
		},
		{
			"package",
			"publish",
			"go",
		},
	} {
		if c, rest, err := root.Find(path); err != nil || len(rest) != 0 || c.Name() != path[len(path)-1] {
			t.Errorf("relic %s: %v", strings.Join(path, " "), err)
		}
	}
}

func TestAuthContextShowAndLogout(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.yaml")

	var c cliConfig
	c.set(cliContext{
		Name:  "prod",
		URL:   "https://relic.example",
		User:  "ada",
		Token: "secret-token",
	})
	c.set(cliContext{
		Name:  "dev",
		URL:   "http://127.0.0.1:8090",
		Token: "dev-token",
	})

	if err := saveConfig(path, c); err != nil {
		t.Fatal(err)
	}

	out, err := runRelic(t, nil, "--config", path, "auth", "context", "show")
	if err != nil || !strings.Contains(out, "│ dev ") || strings.Contains(out, "token") {
		t.Fatalf("show = %q, %v", out, err)
	}

	if _, err := runRelic(
		t,
		nil,
		"--config",
		path,
		"--context",
		"staging",
		"auth",
		"context",
		"show",
	); err == nil || !strings.Contains(err.Error(), `no context "staging"`) {
		t.Errorf("show of a missing context = %v", err)
	}

	if _, err := runRelic(t, nil, "--config", path, "auth", "logout"); err != nil {
		t.Fatal(err)
	}

	out, _ = runRelic(t, nil, "--config", path, "auth", "context", "list")
	if strings.Contains(out, "dev") || !strings.Contains(out, "prod") {
		t.Errorf("after logout: %q", out)
	}

	if _, err := runRelic(t, nil, "--config", path, "auth", "logout", "staging"); err == nil {
		t.Error("logging out of a missing context succeeded")
	}
}

func TestServerConfigMasksSecrets(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	settings := filepath.Join(home, ".config", "relic", "server.env")

	svc, err := (&machineSetup{
		home: home,
		goos: runtime.GOOS,
		getenv: func(string) string {
			return ""
		},
	}).serviceManager()
	if err != nil {
		t.Skip(err)
	}

	unit := svc.definition()

	for p, content := range map[string]string{
		unit:     "",
		settings: "RELIC_HTTP_LISTEN_ADDRESS=127.0.0.1:8090\nRELIC_ENCRYPTION_KEY=c2VjcmV0\nRELIC_ADMIN_PASSWORD=hunter2\n",
	} {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	out, err := runRelic(t, map[string]string{
		"HOME": home,
	}, "server", "config")
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(out, "hunter2") || strings.Contains(out, "c2VjcmV0") ||
		!strings.Contains(out, "RELIC_ADMIN_PASSWORD=********") ||
		!strings.Contains(out, "RELIC_HTTP_LISTEN_ADDRESS=127.0.0.1:8090") {
		t.Errorf("server config = %q", out)
	}
}

func TestServerCommandsWithoutAService(t *testing.T) {
	t.Parallel()

	env := map[string]string{
		"HOME": t.TempDir(),
	}

	for _, args := range [][]string{
		{
			"server",
			"status",
		},
		{
			"server",
			"config",
		},
	} {
		if _, err := runRelic(t, env, args...); err == nil || !strings.Contains(err.Error(), "relic server setup") {
			t.Errorf("relic %s: %v", strings.Join(args, " "), err)
		}
	}
}
