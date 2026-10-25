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

func TestUpdateRepositoryKeepsTheRemotePasswordOnItsHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		url          string
		password     string
		wantErr      error
		wantPassword string
	}{
		{
			name:         "the same host, another path",
			url:          "https://upstream-a.example.com/other/",
			wantPassword: "stored-secret",
		},
		{
			name:    "another host",
			url:     "https://upstream-b.example.com/",
			wantErr: ErrInvalidRepository,
		},
		{
			name:    "another port",
			url:     "https://upstream-a.example.com:8443/",
			wantErr: ErrInvalidRepository,
		},
		{
			name:    "another scheme",
			url:     "http://upstream-a.example.com/",
			wantErr: ErrInvalidRepository,
		},
		{
			name:         "another host with the password given again",
			url:          "https://upstream-b.example.com/",
			password:     "new-secret",
			wantPassword: "new-secret",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			s, _, _ := newTestService(t)

			if err := s.CreateRepository(ctx, Repository{
				Name:   "up",
				Class:  ClassRemote,
				Format: "generic",
				Config: Config{
					Remote: &RemoteConfig{
						URL:      "https://upstream-a.example.com/",
						Username: "svc",
						Password: "stored-secret",
					},
				},
			}); err != nil {
				t.Fatal(err)
			}

			err := s.UpdateRepository(ctx, Repository{
				Name:   "up",
				Class:  ClassRemote,
				Format: "generic",
				Config: Config{
					Remote: &RemoteConfig{
						URL:      tt.url,
						Username: "svc",
						Password: tt.password,
					},
				},
			})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("UpdateRepository to %s = %v, want %v", tt.url, err, tt.wantErr)
			}

			stored, err := s.storedPassword(ctx, "up")
			if err != nil {
				t.Fatal(err)
			}

			if tt.wantErr == nil && stored != tt.wantPassword {
				t.Errorf("stored password = %q, want %q", stored, tt.wantPassword)
			}
		})
	}
}
