package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildFlagIsCheckedBeforeAnyUpload(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	f := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(f, []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{
			"artifact",
			"upload",
			f,
			"libs-local/a.txt",
			"--build",
			"badformat",
		},
		{
			"package",
			"publish",
			"go",
			"go-local",
			"v1.0.0",
			"--dir",
			dir,
			"--build",
			"badformat",
		},
	} {
		_, err := runRelic(
			t,
			map[string]string{
				"RELIC_CONFIG": filepath.Join(dir, "missing.yaml"),
			},
			append([]string{
				"--config",
				filepath.Join(dir, "missing.yaml"),
			}, args...)...,
		)
		if err == nil || !strings.Contains(err.Error(), "<name>/<number>") {
			t.Errorf("relic %s = %v, want the --build format refused", strings.Join(args, " "), err)
		}
	}
}

func TestSplitTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		target   string
		wantRepo string
		wantPath string
		wantErr  bool
	}{
		{
			name:     "a file",
			target:   "libs-local/org/app.jar",
			wantRepo: "libs-local",
			wantPath: "org/app.jar",
		},
		{
			name:     "surrounding slashes",
			target:   "/libs-local/org/",
			wantRepo: "libs-local",
			wantPath: "org",
		},
		{
			name:    "no path",
			target:  "libs-local",
			wantErr: true,
		},
		{
			name:    "a dot segment",
			target:  "libs-local/x/./y.txt",
			wantErr: true,
		},
		{
			name:    "an empty segment",
			target:  "libs-local/x//y.txt",
			wantErr: true,
		},
		{
			name:    "a parent segment",
			target:  "libs-local/../escape.txt",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			repo, p, err := splitTarget(tt.target)
			if (err != nil) != tt.wantErr {
				t.Fatalf("splitTarget(%q) error = %v, want error %v", tt.target, err, tt.wantErr)
			}

			if repo != tt.wantRepo || p != tt.wantPath {
				t.Errorf("splitTarget(%q) = %q, %q; want %q, %q", tt.target, repo, p, tt.wantRepo, tt.wantPath)
			}
		})
	}
}
