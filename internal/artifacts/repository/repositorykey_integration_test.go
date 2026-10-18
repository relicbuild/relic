//go:build integration

package repository

import (
	"errors"
	"strings"
	"testing"
)

func TestCreateRepositoryRefusesKeysThatDifferOnlyByCase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		key     string
		wantErr error
	}{
		{
			name:    "same key",
			key:     "generic-local",
			wantErr: ErrRepositoryExists,
		},
		{
			name:    "key in other case",
			key:     "Generic-Local",
			wantErr: ErrRepositoryKeyCaseConflict,
		},
		{
			name: "another key",
			key:  "generic-remote-cache",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, _, _ := newTestService(t)

			err := s.CreateRepository(t.Context(), Repository{
				Name:   tt.key,
				Class:  ClassLocal,
				Format: "generic",
			})
			if !errors.Is(err, tt.wantErr) || (tt.wantErr == nil && err != nil) {
				t.Errorf("CreateRepository(%q) = %v, want %v", tt.key, err, tt.wantErr)
			}
		})
	}
}

func TestTrashRepositoryInAnotherCaseIsReported(t *testing.T) {
	t.Parallel()

	s, _, _ := newTestService(t)

	if err := s.CreateRepository(t.Context(), Repository{
		Name:   strings.ToUpper(s.TrashRepository()),
		Class:  ClassLocal,
		Format: FormatGeneric,
	}); err != nil {
		t.Fatal(err)
	}

	if err := s.ensureTrashRepo(t.Context()); !errors.Is(err, ErrRepositoryKeyCaseConflict) {
		t.Errorf("ensureTrashRepo() = %v, want %v", err, ErrRepositoryKeyCaseConflict)
	}
}
