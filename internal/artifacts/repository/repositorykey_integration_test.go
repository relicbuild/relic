//go:build integration

package repository

import (
	"errors"
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
			wantErr: ErrRepositoryExists,
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
