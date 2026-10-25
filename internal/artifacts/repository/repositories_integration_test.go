//go:build integration

package repository

import (
	"errors"
	"testing"
	"time"

	"relic/internal/access/project"
	"relic/internal/platform/database"
)

func TestRepositoriesNameAProjectThatExists(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		project string
		wantErr error
	}{
		{
			name: "no project",
		},
		{
			name:    "the default project",
			project: project.DefaultKey,
		},
		{
			name:    "an existing project",
			project: "acme",
		},
		{
			name:    "a project that does not exist",
			project: "ghostproj",
			wantErr: ErrInvalidRepository,
		},
		{
			name:    "an invalid key",
			project: "Bad Key/../x",
			wantErr: ErrInvalidRepository,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			s, _, db := newTestService(t)

			if err := db.UpsertProject(ctx, database.UpsertProjectParams{
				Key:    "acme",
				Config: []byte(`{"key":"acme","name":"acme","members":[]}`),
				Now:    time.Now().UTC(),
			}); err != nil {
				t.Fatal(err)
			}

			err := s.CreateRepository(ctx, Repository{
				Name:    "libs",
				Class:   ClassLocal,
				Format:  "generic",
				Project: tt.project,
			})
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("CreateRepository in project %q = %v, want %v", tt.project, err, tt.wantErr)
			}

			err = s.UpdateRepository(ctx, Repository{
				Name:    "generic-local",
				Class:   ClassLocal,
				Format:  "generic",
				Project: tt.project,
			})
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("UpdateRepository into project %q = %v, want %v", tt.project, err, tt.wantErr)
			}
		})
	}
}

func TestDeletingAProjectWithRepositoriesIsRefused(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	s, _, db := newTestService(t)
	projects := project.New(db)
	projects.SetRepositoryProjects(s.Projects)

	if _, err := projects.Put(ctx, project.Project{
		Key: "tmpp",
	}); err != nil {
		t.Fatal(err)
	}

	if err := s.CreateRepository(ctx, Repository{
		Name:    "tmpp-r",
		Class:   ClassLocal,
		Format:  "generic",
		Project: "tmpp",
	}); err != nil {
		t.Fatal(err)
	}

	if err := projects.Delete(ctx, "tmpp"); !errors.Is(err, project.ErrHasRepositories) {
		t.Fatalf("Delete of a project with a repository = %v, want %v", err, project.ErrHasRepositories)
	}

	if err := s.UpdateRepository(ctx, Repository{
		Name:   "tmpp-r",
		Class:  ClassLocal,
		Format: "generic",
	}); err != nil {
		t.Fatal(err)
	}

	if err := projects.Delete(ctx, "tmpp"); err != nil {
		t.Errorf("Delete of a project without repositories = %v", err)
	}

	if err := s.UpdateRepository(ctx, Repository{
		Name:    "tmpp-r",
		Class:   ClassLocal,
		Format:  "generic",
		Project: "tmpp",
	}); !errors.Is(err, ErrInvalidRepository) {
		t.Errorf("UpdateRepository into a deleted project = %v, want %v", err, ErrInvalidRepository)
	}
}
