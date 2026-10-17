package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestEveryRepositoryPathArgumentCompletes(t *testing.T) {
	t.Parallel()

	root := newRootCmd(func(string) string {
		return ""
	})

	var walk func(*cobra.Command)

	walk = func(c *cobra.Command) {
		if strings.Contains(c.Use, "<repository>/<path>") && c.ValidArgsFunction == nil {
			t.Errorf("%q takes <repository>/<path> but does not complete it", c.CommandPath())
		}

		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
}

func TestCompleteTargetArgLeavesOtherArgumentsToTheShell(t *testing.T) {
	t.Parallel()

	complete := completeTargetArg(&globals{}, 1)

	if out, directive := complete(&cobra.Command{}, nil, "app"); out != nil ||
		directive != cobra.ShellCompDirectiveDefault {
		t.Errorf("first argument = %v %v, want the shell's file completion", out, directive)
	}

	if _, directive := complete(&cobra.Command{}, []string{
		"app.jar",
		"libs/app.jar",
	}, ""); directive != cobra.ShellCompDirectiveDefault {
		t.Errorf("third argument = %v, want the shell's file completion", directive)
	}
}
