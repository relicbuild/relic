package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"relic/internal/platform/scripttest"
)

func TestGenerateWritesNothingWhenInterrupted(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	jf := filepath.Join(dir, "jf")
	scripttest.Write(t, jf, `#!/bin/sh
case "$1" in
--version) echo "jf version 2.124.0" ;;
completion) echo "complete -c jf -n '__fish_use_subcommand' -a rt" ;;
*) while :; do :; done ;;
esac
`)

	rules := filepath.Join(dir, "tiers.yaml")
	if err := os.WriteFile(rules, []byte("rules: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	inventory := filepath.Join(dir, "commands.yaml")

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	if err := generate(ctx, jf, rules, inventory); err == nil {
		t.Error("generate after an interruption returned no error")
	}

	if _, err := os.Stat(inventory); err == nil {
		t.Error("generate wrote an inventory after an interruption")
	}
}
