package main

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDatabaseCommandsRefuseAMissingSQLiteSource(t *testing.T) {
	t.Parallel()

	for name, args := range map[string][]string{
		"backup": {
			"backup",
			"--database-url",
			"sqlite://{dir}/ghost.db",
			"{dir}/backup.db",
		},
		"copy": {
			"copy",
			"--from",
			"sqlite://{dir}/ghost.db",
			"--to",
			"sqlite://{dir}/copy.db",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()

			root := newRootCmd(func(string) string {
				return ""
			})

			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)

			argv := []string{
				"--config",
				filepath.Join(dir, "config.yaml"),
				"server",
				"database",
			}
			for _, a := range args {
				argv = append(argv, strings.ReplaceAll(a, "{dir}", dir))
			}

			root.SetArgs(argv)

			err := root.ExecuteContext(t.Context())
			if err == nil || !strings.Contains(err.Error(), "does not exist") {
				t.Errorf("%s of a missing database = %v, output %q", name, err, out.String())
			}

			for _, file := range []string{
				"ghost.db",
				"backup.db",
			} {
				if _, err := os.Stat(filepath.Join(dir, file)); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("%s created %s", name, file)
				}
			}
		})
	}
}
