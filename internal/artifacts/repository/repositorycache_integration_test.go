//go:build integration

package repository

import (
	"errors"
	"testing"

	"relic/internal/platform/database"
)

func TestGetRepositoryServesRepeatedReadsFromMemory(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	s, _, db := newTestService(t)

	if _, err := s.GetRepository(ctx, "generic-local"); err != nil {
		t.Fatal(err)
	}

	if err := db.UpdateRepository(ctx, database.UpdateRepositoryParams{
		Key:     "generic-local",
		Format:  "generic",
		Config:  []byte(`{"description":"changed behind the service"}`),
		Project: "",
	}); err != nil {
		t.Fatal(err)
	}

	r, err := s.GetRepository(ctx, "generic-local")
	if err != nil || r.Config.Description != "" {
		t.Errorf("second read = %q, %v; want the cached settings", r.Config.Description, err)
	}
}

func TestRepositoryWritesInvalidateTheCache(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		write func(t *testing.T, s *Service)
		check func(t *testing.T, s *Service)
	}{
		{
			name: "update",
			write: func(t *testing.T, s *Service) {
				t.Helper()

				if err := s.UpdateRepository(t.Context(), Repository{
					Name:   "generic-local",
					Class:  ClassLocal,
					Format: FormatGeneric,
					Config: Config{
						Description: "updated",
					},
				}); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, s *Service) {
				t.Helper()

				r, err := s.GetRepository(t.Context(), "generic-local")
				if err != nil || r.Config.Description != "updated" {
					t.Errorf("after the update = %q, %v", r.Config.Description, err)
				}

				item, err := s.Stat(t.Context(), "generic-local", "")
				if err != nil || item.Modified.IsZero() {
					t.Errorf("root after the update = %+v, %v", item, err)
				}
			},
		},
		{
			name: "delete",
			write: func(t *testing.T, s *Service) {
				t.Helper()

				if err := s.DeleteRepository(t.Context(), "generic-local"); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, s *Service) {
				t.Helper()

				if _, err := s.GetRepository(t.Context(), "generic-local"); !errors.Is(err, ErrRepositoryNotFound) {
					t.Errorf("after the delete = %v, want ErrRepositoryNotFound", err)
				}

				if _, err := s.Stat(t.Context(), "generic-local", ""); !errors.Is(err, ErrRepositoryNotFound) {
					t.Errorf("stat after the delete = %v, want ErrRepositoryNotFound", err)
				}
			},
		},
		{
			name: "delete of a member",
			write: func(t *testing.T, s *Service) {
				t.Helper()

				if err := s.DeleteRepository(t.Context(), "generic-local"); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, s *Service) {
				t.Helper()

				r, err := s.GetRepository(t.Context(), "generic-virtual")
				if err != nil || len(r.Members()) != 0 {
					t.Errorf("virtual after its member's delete = %v, %v; want no members", r.Members(), err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, _, _ := newTestService(t)
			createRepo(t, s, virtualRepo("generic-virtual", "", "generic-local"))

			for _, key := range []string{
				"generic-local",
				"generic-virtual",
			} {
				if _, err := s.GetRepository(t.Context(), key); err != nil {
					t.Fatal(err)
				}
			}

			tt.write(t, s)
			tt.check(t, s)
		})
	}
}

func TestGetRepositoryReturnsACopyOfTheCachedSettings(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	s, _, _ := newTestService(t)
	createRepo(t, s, virtualRepo("generic-virtual", "", "generic-local"))

	first, err := s.GetRepository(ctx, "generic-virtual")
	if err != nil {
		t.Fatal(err)
	}

	first.Config.Virtual.Members[0] = "changed"

	second, err := s.GetRepository(ctx, "generic-virtual")
	if err != nil || second.Members()[0] != "generic-local" {
		t.Errorf("members after changing a returned copy = %v, %v", second.Members(), err)
	}
}
