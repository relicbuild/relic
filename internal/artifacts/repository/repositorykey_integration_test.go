//go:build integration

package repository

import (
	"errors"
	"slices"
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

func TestOwnRepositories(t *testing.T) {
	t.Parallel()

	s, _, _ := newTestService(t)

	for _, r := range []Repository{
		{
			Name:   "builds",
			Class:  ClassLocal,
			Format: FormatBuild,
		},
		{
			Name:   "releases",
			Class:  ClassLocal,
			Format: FormatRelease,
		},
		{
			Name:   "attestations",
			Class:  ClassLocal,
			Format: FormatEvidence,
		},
	} {
		if err := s.CreateRepository(t.Context(), r); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.OwnRepositories(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	slices.Sort(got)

	want := []string{
		"attestations",
		"builds",
		"releases",
	}
	if !slices.Equal(got, want) {
		t.Errorf("OwnRepositories = %v, want %v", got, want)
	}
}
