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

func TestServerNamesComplete(t *testing.T) {
	t.Parallel()

	root := newRootCmd(func(string) string {
		return ""
	})

	for _, a := range argumentCompletions() {
		cmd, _, err := root.Find(strings.Fields(a.path))
		if err != nil || cmd.CommandPath() != "relic "+a.path {
			t.Errorf("relic %s is not a command: %v", a.path, err)

			continue
		}

		if cmd.ValidArgsFunction == nil {
			t.Errorf("relic %s does not complete its arguments", a.path)
		}
	}

	var walk func(*cobra.Command)

	walk = func(c *cobra.Command) {
		if len(c.ValidArgs) > 0 && c.ValidArgsFunction != nil {
			t.Errorf("%s has ValidArgs, which hide its ValidArgsFunction", c.CommandPath())
		}

		for _, f := range flagCompletions() {
			if c.Flags().Lookup(f.flag) == nil || namesAnotherServer(c) {
				continue
			}

			if _, ok := c.GetFlagCompletionFunc(f.flag); !ok {
				t.Errorf("%s --%s does not complete", c.CommandPath(), f.flag)
			}
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
