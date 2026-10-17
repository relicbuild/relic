package buildinfo

import (
	"testing"

	"relic/internal/artifacts/repository"
)

func TestLayoutStorageOwner(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		repository  string
		wantProject string
		wantOK      bool
	}{
		{
			name:       "builds outside projects",
			repository: "relic-build-info",
			wantOK:     true,
		},
		{
			name:        "a project's builds",
			repository:  "alpha-relic-build-info",
			wantProject: "alpha",
			wantOK:      true,
		},
		{
			name:       "a prefix that is not a project key",
			repository: "my-app-relic-build-info",
		},
		{
			name:       "the default project's name",
			repository: "default-relic-build-info",
		},
		{
			name:       "another repository",
			repository: "libs-local",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			format, project, ok := DefaultLayout.StorageOwner(tt.repository)
			if ok != tt.wantOK || project != tt.wantProject {
				t.Fatalf(
					"StorageOwner(%q) = %q, %t; want %q, %t",
					tt.repository,
					project,
					ok,
					tt.wantProject,
					tt.wantOK,
				)
			}

			if ok && format != repository.FormatBuild {
				t.Errorf("StorageOwner(%q) format = %q, want %q", tt.repository, format, repository.FormatBuild)
			}
		})
	}
}
