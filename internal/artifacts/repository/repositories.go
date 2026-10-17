package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"relic/internal/artifacts/signingkey"
	"relic/internal/platform/database"
)

// Repository is a repository's identity and settings.
type Repository struct {
	Name    string
	Class   Class
	Format  Format
	Project string
	Config  Config
	Created time.Time
	Updated time.Time
}

// CreateRepository stores a new repository.
func (s *Service) CreateRepository(ctx context.Context, r Repository) error {
	if err := r.Validate(); err != nil {
		return err
	}

	config, err := s.encodeConfig(r)
	if err != nil {
		return err
	}

	err = s.db.CreateRepository(ctx, database.CreateRepositoryParams{
		Key:     r.Name,
		Class:   string(r.Class),
		Format:  string(r.Format),
		Project: r.Project,
		Config:  config,
	})
	if database.IsUniqueViolation(err) {
		return ErrRepositoryExists
	}

	if err != nil {
		return fmt.Errorf("create repository %s: %w", r.Name, err)
	}

	return nil
}

// UpdateRepository replaces a repository's settings.
func (s *Service) UpdateRepository(ctx context.Context, r Repository) error {
	if err := r.Validate(); err != nil {
		return err
	}

	existing, err := s.GetRepository(ctx, r.Name)
	if err != nil {
		return err
	}

	if existing.Class != r.Class {
		return fmt.Errorf("%w: cannot change the class from %s to %s", ErrInvalidRepository, existing.Class, r.Class)
	}

	if r.Config.Remote != nil && r.Config.Remote.Password == "" && existing.Config.Remote != nil {
		stored, err := s.storedPassword(ctx, r.Name)
		if err != nil {
			return err
		}

		r.Config.Remote.Password = stored
	}

	config, err := s.encodeConfig(r)
	if err != nil {
		return err
	}

	err = s.db.UpdateRepository(
		ctx,
		database.UpdateRepositoryParams{
			Key:     r.Name,
			Format:  string(r.Format),
			Project: r.Project,
			Config:  config,
		},
	)
	if err != nil {
		return fmt.Errorf("update repository %s: %w", r.Name, err)
	}

	return nil
}

// GetRepository returns one repository.
func (s *Service) GetRepository(ctx context.Context, key string) (Repository, error) {
	row, err := s.db.GetRepository(ctx, key)
	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		return Repository{}, ErrRepositoryNotFound
	}

	if err != nil {
		return Repository{}, fmt.Errorf("get repository %s: %w", key, err)
	}

	return s.repositoryFromRow(row), nil
}

// QuarantinedRepositories names the remote repositories that quarantine
// what they cache.
func (s *Service) QuarantinedRepositories(ctx context.Context) ([]string, error) {
	remotes, err := s.ListRepositories(ctx, ClassRemote, "")
	if err != nil {
		return nil, err
	}

	var out []string

	for _, r := range remotes {
		if r.Upstream().Quarantine {
			out = append(out, r.Name)
		}
	}

	return out, nil
}

// ListRepositories returns repositories, optionally filtered by class and
// format (empty values match all).
func (s *Service) ListRepositories(ctx context.Context, class Class, format Format) ([]Repository, error) {
	var params database.ListRepositoriesParams
	if class != "" {
		params.Class = (*string)(&class)
	}

	if format != "" {
		params.Format = (*string)(&format)
	}

	rows, err := s.db.ListRepositories(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list repositories: %w", err)
	}

	repos := make([]Repository, 0, len(rows))
	for _, r := range rows {
		if r.Key == s.trashLayout.Repository {
			continue
		}

		repos = append(repos, s.repositoryFromRow(r))
	}

	return repos, nil
}

// DeleteRepository removes a repository and all its items.
func (s *Service) DeleteRepository(ctx context.Context, key string) error {
	return s.db.WithTx(ctx, func(q database.Querier) error {
		if err := checkHolds(ctx, q, key, ""); err != nil {
			return err
		}

		shas, err := q.DeleteRepositoryItems(ctx, key)
		if err != nil {
			return fmt.Errorf("delete items of %s: %w", key, err)
		}

		if err := releaseAll(ctx, q, shas); err != nil {
			return err
		}

		n, err := q.DeleteRepository(ctx, key)
		if err != nil {
			return fmt.Errorf("delete repository %s: %w", key, err)
		}

		if n == 0 {
			return ErrRepositoryNotFound
		}

		return nil
	})
}

func (s *Service) repositoryFromRow(row database.Repository) Repository {
	r := Repository{
		Name:    row.Key,
		Class:   Class(row.Class),
		Format:  Format(row.Format),
		Project: row.Project,
		Created: row.Created,
		Updated: row.Updated,
	}
	if err := json.Unmarshal(row.Config, &r.Config); err != nil {
		s.log.Error("repository settings do not decode", "repo", row.Key, "error", err)
	}

	return s.openPassword(r)
}

// KeyPairRef names the key pair that signs the repository's metadata: its
// signing key, else Relic's default for Alpine (RSA), and Debian and RPM
// (OpenPGP) repositories, else "".
func (r Repository) KeyPairRef() string {
	if r.Config.SigningKey != "" {
		return r.Config.SigningKey
	}

	switch r.Format {
	case FormatAlpine:
		return signingkey.DefaultRSA
	case FormatDebian, FormatRPM:
		return signingkey.DefaultGPG
	default:
		return ""
	}
}
