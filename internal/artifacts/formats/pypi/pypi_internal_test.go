package pypi

import "testing"

func TestFilenameMatches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		file          string
		project       string
		version       string
		wantsAccepted bool
	}{
		{
			name:          "sdist",
			file:          "demo-1.0.0.tar.gz",
			project:       "demo",
			version:       "1.0.0",
			wantsAccepted: true,
		},
		{
			name:          "wheel with an escaped project name",
			file:          "my_pkg-1.0.0-py3-none-any.whl",
			project:       "My-Pkg",
			version:       "1.0.0",
			wantsAccepted: true,
		},
		{
			name:          "sdist of a project with dashes",
			file:          "python-dateutil-2.8.2.tar.gz",
			project:       "python_dateutil",
			version:       "2.8.2",
			wantsAccepted: true,
		},
		{
			name:          "legacy sdist with a dash in its version",
			file:          "demo-1.0-beta.zip",
			project:       "demo",
			version:       "1.0-beta",
			wantsAccepted: true,
		},
		{
			name:          "egg",
			file:          "demo-1.0-py3.8.egg",
			project:       "demo",
			version:       "1.0",
			wantsAccepted: true,
		},
		{
			name:          "wheel with an escaped version",
			file:          "demo-1.0_dev1-py3-none-any.whl",
			project:       "demo",
			version:       "1.0-dev1",
			wantsAccepted: true,
		},
		{
			name:    "another project",
			file:    "other_lib-9.9.tar.gz",
			project: "demo",
			version: "9.9",
		},
		{
			name:    "another version",
			file:    "demo-9.9.tar.gz",
			project: "demo",
			version: "2.0",
		},
		{
			name:    "a project whose name starts with the uploaded one",
			file:    "demo-extra-1.0.tar.gz",
			project: "demo",
			version: "1.0",
		},
		{
			name:    "wheel of another version",
			file:    "demo-9.9-py3-none-any.whl",
			project: "demo",
			version: "2.0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := filenameMatches(tt.file, tt.project, tt.version); got != tt.wantsAccepted {
				t.Errorf("filenameMatches(%q, %q, %q) = %v, want %v", tt.file, tt.project, tt.version, got,
					tt.wantsAccepted)
			}
		})
	}
}
