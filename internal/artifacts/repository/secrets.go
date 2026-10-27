package repository

import (
	"context"
	"encoding/json"
	"fmt"

	"relic/internal/platform/database"
	"relic/internal/platform/sealing"
)

// SetSecrets sets the box that seals remote repositories' upstream
// passwords at rest.
func (s *Service) SetSecrets(box *sealing.Box) { s.box = box }

func passwordPlace(key string) string { return "repositories/" + key + "/password" }

func (s *Service) encodeConfig(r Repository) ([]byte, error) {
	c := r.Config
	if rc := c.Remote; rc != nil && rc.Password != "" && s.box.Enabled() && !sealing.SealedString(rc.Password) {
		sealed, err := s.box.SealString(rc.Password, passwordPlace(r.Name))
		if err != nil {
			return nil, fmt.Errorf("seal the password of %s: %w", r.Name, err)
		}

		remote := *rc
		remote.Password = sealed
		c.Remote = &remote
	}

	b, err := json.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("encode repository %s: %w", r.Name, err)
	}

	return b, nil
}

// storedPassword is a remote repository's upstream password as stored, still
// sealed: an update without a password keeps it even when it does not open.
func (s *Service) storedPassword(ctx context.Context, key string) (string, error) {
	row, err := s.db.GetRepository(ctx, key)
	if err != nil {
		return "", fmt.Errorf("get repository %s: %w", key, err)
	}

	var c Config
	if err := json.Unmarshal(row.Config, &c); err != nil {
		return "", fmt.Errorf("decode repository %s: %w", key, err)
	}

	if c.Remote == nil {
		return "", nil
	}

	return c.Remote.Password, nil
}

func (s *Service) openPassword(r Repository) Repository {
	rc := r.Config.Remote
	if rc == nil || !sealing.SealedString(rc.Password) {
		return r
	}

	plain, err := s.box.OpenString(rc.Password, passwordPlace(r.Name))
	if err != nil {
		s.log.Warn("upstream password does not open; the remote runs without it", "repo", r.Name, "error", err)

		plain = ""
	}

	remote := *rc
	remote.Password = plain
	r.Config.Remote = &remote

	return r
}

// SealStoredPasswords seals upstream passwords stored before
// RELIC_ENCRYPTION_KEY was set.
func (s *Service) SealStoredPasswords(ctx context.Context) error {
	if !s.box.Enabled() {
		return nil
	}

	rows, err := s.db.ListRepositories(ctx, database.ListRepositoriesParams{})
	if err != nil {
		return fmt.Errorf("list repositories: %w", err)
	}

	defer s.repositories.invalidate()

	for _, row := range rows {
		var c Config
		if err := json.Unmarshal(row.Config, &c); err != nil {
			return fmt.Errorf("decode repository %s: %w", row.Key, err)
		}

		if c.Remote == nil || c.Remote.Password == "" || sealing.SealedString(c.Remote.Password) {
			continue
		}

		sealed, err := s.encodeConfig(Repository{
			Name:   row.Key,
			Config: c,
		})
		if err != nil {
			return err
		}

		if err := s.db.UpdateRepository(
			ctx,
			database.UpdateRepositoryParams{
				Key:     row.Key,
				Format:  row.Format,
				Project: row.Project,
				Config:  sealed,
			},
		); err != nil {
			return fmt.Errorf("seal password of %s: %w", row.Key, err)
		}
	}

	return nil
}
