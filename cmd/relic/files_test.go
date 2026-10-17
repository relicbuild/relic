package main

import (
	"testing"
)

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
