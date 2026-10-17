package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func setupTestCommand(t *testing.T) (*cobra.Command, *bytes.Buffer) {
	t.Helper()

	var out bytes.Buffer

	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	return cmd, &out
}

func applySetupSteps(t *testing.T, client string, e setupEnv) error {
	t.Helper()

	cmd, _ := setupTestCommand(t)

	steps, err := setupClients[client].steps(e)
	if err != nil {
		return err
	}

	for _, s := range steps {
		if err := applySetupStep(cmd, e, setupClients[client], s, false); err != nil {
			return err
		}
	}

	return nil
}

func TestPrintedSetupCommandsAreShell(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("checks the commands with sh")
	}

	e := setupEnv{
		repo:  "gems-local",
		url:   "https://relic.example.com/gems/gems-local/",
		user:  "ci",
		token: "<token>",
		home:  t.TempDir(),
		flags: map[string]string{
			"private": "git.example.com/*",
		},
	}

	for _, client := range []string{
		"ruby",
		"go",
		"conan",
		"nuget",
	} {
		steps, err := setupClients[client].steps(e)
		if err != nil {
			t.Fatal(err)
		}

		var out bytes.Buffer
		for _, s := range steps {
			printSetupStep(&out, e, setupClients[client], s)
		}

		for line := range strings.Lines(out.String()) {
			command, ok := strings.CutPrefix(strings.TrimSuffix(line, "\n"), "$ ")
			if !ok {
				continue
			}

			if err := exec.CommandContext(t.Context(), "sh", "-n", "-c", command).Run(); err != nil {
				t.Errorf("%s: %q is not a shell command: %v", client, command, err)
				continue
			}

			_, last, _ := strings.Cut(command, "| ")
			if client != "ruby" || !strings.HasPrefix(last, "ruby ") {
				continue
			}

			words, err := exec.CommandContext(t.Context(), "sh", "-c", `set -f; printf '%s\n' `+last).Output()
			if err != nil ||
				!strings.Contains(string(words), "\nBundler.settings.set_global(ARGV[0], $stdin.read.chomp)\n") {
				t.Errorf("ruby: %q does not pass the script as one word: %q %v", command, words, err)
			}
		}
	}

	if note := setupClients["ruby"].notes(e); !strings.Contains(note, "https://ci:<token>@relic.example.com/") {
		t.Errorf("gem note = %q", note)
	}
}

func TestSetupWritesWhereTheClientReads(t *testing.T) {
	t.Parallel()

	withPipDirectory := t.TempDir()
	if err := os.MkdirAll(filepath.Join(withPipDirectory, "Library", "Application Support", "pip"), 0o700); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		client, goos, home, want string
	}{
		{
			client: "pip",
			goos:   "linux",
			home:   t.TempDir(),
			want:   ".config/pip/pip.conf",
		},
		{
			client: "pip",
			goos:   "darwin",
			home:   t.TempDir(),
			want:   ".config/pip/pip.conf",
		},
		{
			client: "pip",
			goos:   "darwin",
			home:   withPipDirectory,
			want:   "Library/Application Support/pip/pip.conf",
		},
		{
			client: "pip",
			goos:   "windows",
			home:   t.TempDir(),
			want:   "AppData/Roaming/pip/pip.ini",
		},
		{
			client: "terraform",
			goos:   "linux",
			home:   t.TempDir(),
			want:   ".terraformrc",
		},
		{
			client: "terraform",
			goos:   "windows",
			home:   t.TempDir(),
			want:   "AppData/Roaming/terraform.rc",
		},
	} {
		steps, err := setupClients[tc.client].steps(setupEnv{
			goos: tc.goos,
			home: tc.home,
		})
		if err != nil {
			t.Fatal(err)
		}

		if steps[0].file != tc.want {
			t.Errorf("%s on %s writes %s, want %s", tc.client, tc.goos, steps[0].file, tc.want)
		}
	}
}

func TestSetupCommandsRunUnderHome(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("the command is a shell script")
	}

	home, seen := t.TempDir(), t.TempDir()
	cmd, _ := setupTestCommand(t)

	err := applySetupStep(cmd, setupEnv{
		home: home,
	}, setupClient{}, setupStep{
		command: []string{
			"sh",
			"-c",
			`printf %s "$HOME" > "$0/home"; printf %s "${XDG_CONFIG_HOME-unset}" > "$0/config"`,
			seen,
		},
	}, false)
	if err != nil {
		t.Fatal(err)
	}

	for file, want := range map[string]string{
		"home":   home,
		"config": "unset",
	} {
		if b, err := os.ReadFile(filepath.Join(seen, file)); err != nil || string(b) != want {
			t.Errorf("%s = %q %v, want %q", file, b, err, want)
		}
	}
}

func TestNuGetSetupRunsAgain(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in dotnet is a shell script")
	}

	bin := t.TempDir()
	sources := filepath.Join(t.TempDir(), "sources")
	dotnet := `#!/bin/sh
for last; do :; done
case "$1 $2" in
"nuget remove")
	grep -qx "$4" "` + sources + `" 2>/dev/null || { echo "no such source" >&2; exit 1; }
	grep -vx "$4" "` + sources + `" > "` + sources + `.new"; mv "` + sources + `.new" "` + sources + `" ;;
*)
	name=$(sed -n '/^--name$/{n;p;}' "${last#@}")
	grep -qx "$name" "` + sources + `" 2>/dev/null && { echo "The name specified has already been added" >&2; exit 1; }
	echo "$name" >> "` + sources + `" ;;
esac
`

	//nolint:gosec // a stand-in command must be executable
	if err := os.WriteFile(filepath.Join(bin, "dotnet"), []byte(dotnet), 0o700); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	e := setupEnv{
		repo:  "nuget-local",
		url:   "https://relic.example.com/nuget/nuget-local/index.json",
		user:  "ci",
		token: "secret",
		home:  t.TempDir(),
	}

	for run := range 2 {
		if err := applySetupSteps(t, "nuget", e); err != nil {
			t.Fatalf("run %d: %v", run+1, err)
		}
	}

	if b, _ := os.ReadFile(sources); strings.Count(string(b), "nuget-local\n") != 1 {
		t.Errorf("sources = %q", b)
	}
}
