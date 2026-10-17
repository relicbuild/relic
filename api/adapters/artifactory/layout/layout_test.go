package layout

import (
	"context"
	"errors"
	"testing"

	"relic/internal/artifacts/repository"
)

type repositoryMap map[string]error

func (m repositoryMap) GetRepository(_ context.Context, name string) (repository.Repository, error) {
	err, ok := m[name]
	if !ok {
		return repository.Repository{}, repository.ErrRepositoryNotFound
	}

	if err != nil {
		return repository.Repository{}, err
	}

	class := repository.ClassLocal
	if name == "npmjs" || name == "flaky" {
		class = repository.ClassRemote
	}

	return repository.Repository{
		Name:  name,
		Class: class,
	}, nil
}

func TestRemoteOfCache(t *testing.T) {
	t.Parallel()

	outage := errors.New("connection reset")
	repos := repositoryMap{
		"npmjs":       nil,
		"libs":        nil,
		"local-cache": nil,
		"flaky-cache": outage,
	}

	tests := []struct {
		name       string
		repos      repositoryMap
		key        string
		wantRemote string
		wantOK     bool
		wantErr    error
	}{
		{
			name:       "a remote's cache",
			repos:      repos,
			key:        "npmjs-cache",
			wantRemote: "npmjs",
			wantOK:     true,
		},
		{
			name:  "a local repository's name with the suffix",
			repos: repos,
			key:   "libs-cache",
		},
		{
			name:  "a real repository named like a cache",
			repos: repos,
			key:   "local-cache",
		},
		{
			name:  "no suffix",
			repos: repos,
			key:   "npmjs",
		},
		{
			name:    "the cache name cannot be looked up",
			repos:   repos,
			key:     "flaky-cache",
			wantErr: outage,
		},
		{
			name: "the remote cannot be looked up",
			repos: repositoryMap{
				"npmjs": outage,
			},
			key:     "npmjs-cache",
			wantErr: outage,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			remote, ok, err := RemoteOfCache(t.Context(), tt.repos, tt.key)
			if remote != tt.wantRemote || ok != tt.wantOK || !errors.Is(err, tt.wantErr) {
				t.Errorf(
					"RemoteOfCache(%q) = %q, %v, %v, want %q, %v, %v",
					tt.key,
					remote,
					ok,
					err,
					tt.wantRemote,
					tt.wantOK,
					tt.wantErr,
				)
			}
		})
	}
}
