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
