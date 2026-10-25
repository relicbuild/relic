package replication

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"relic/internal/artifacts/repository"
	"relic/internal/platform/database"
	"relic/internal/platform/sealing"
)

// Replication is a stored push replication and how its last run went.
type Replication struct {
	Config
	HasPassword bool
	LastRun     *time.Time
	LastStatus  Status
	LastError   string
}

// Service stores push replications.
type Service struct {
	db  *database.DB
	box *sealing.Box
}

// NewService returns a Service.
func NewService(db *database.DB, box *sealing.Box) *Service {
	return &Service{
		db:  db,
		box: box,
	}
}

// Get returns repository's replication; ErrNotConfigured when it has none.
func (s *Service) Get(ctx context.Context, repository string) (Replication, error) {
	row, err := s.db.GetReplication(ctx, repository)
	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		return Replication{}, ErrNotConfigured
	}

	if err != nil {
		return Replication{}, fmt.Errorf("get replication %s: %w", repository, err)
	}

	return fromRow(row)
}

// List returns every replication.
func (s *Service) List(ctx context.Context) ([]Replication, error) {
	rows, err := s.db.ListReplications(ctx)
	if err != nil {
		return nil, fmt.Errorf("list replications: %w", err)
	}

	out := make([]Replication, 0, len(rows))
	for _, row := range rows {
		r, err := fromRow(row)
		if err != nil {
			return nil, err
		}

		out = append(out, r)
	}

	return out, nil
}

func fromRow(row database.Replication) (Replication, error) {
	var cfg Config
	if err := json.Unmarshal(row.Config, &cfg); err != nil {
		return Replication{}, fmt.Errorf("decode replication %s: %w", row.Repository, err)
	}

	has := cfg.Password != ""
	cfg.Password = ""
	cfg.Repository = row.Repository

	return Replication{
		Config:      cfg,
		HasPassword: has,
		LastRun:     row.LastRun,
		LastStatus:  Status(row.LastStatus),
		LastError:   row.LastError,
	}, nil
}

// Save creates or replaces cfg.Repository's replication; an empty password
// keeps the stored one.
func (s *Service) Save(ctx context.Context, cfg Config) error {
	return s.db.WithLock(ctx, replicationLock(cfg.Repository), func() error {
		doc, err := s.document(ctx, cfg)
		if err != nil {
			return err
		}

		if err := s.db.UpsertReplication(
			ctx,
			database.UpsertReplicationParams{
				Repository: cfg.Repository,
				Config:     doc,
			},
		); err != nil {
			return fmt.Errorf("save replication %s: %w", cfg.Repository, err)
		}

		return nil
	})
}

// Update replaces cfg.Repository's replication; ErrNotConfigured when it
// has none.
func (s *Service) Update(ctx context.Context, cfg Config) error {
	return s.db.WithLock(ctx, replicationLock(cfg.Repository), func() error {
		doc, err := s.document(ctx, cfg)
		if err != nil {
			return err
		}

		n, err := s.db.UpdateReplication(
			ctx,
			database.UpdateReplicationParams{
				Repository: cfg.Repository,
				Config:     doc,
			},
		)
		if err != nil {
			return fmt.Errorf("update replication %s: %w", cfg.Repository, err)
		}

		if n == 0 {
			return ErrNotConfigured
		}

		return nil
	})
}

func replicationLock(repository string) string {
	return "replication:" + repository
}

func (s *Service) document(ctx context.Context, cfg Config) ([]byte, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	if cfg.Password == "" {
		row, err := s.db.GetReplication(ctx, cfg.Repository)
		switch {
		case err == nil:
			var old Config
			if err := json.Unmarshal(row.Config, &old); err != nil {
				return nil, fmt.Errorf("decode replication %s: %w", cfg.Repository, err)
			}

			if old.Password != "" && old.TargetURL != cfg.TargetURL &&
				!repository.SameOrigin(old.TargetURL, cfg.TargetURL) {
				return nil, fmt.Errorf("%w: the target URL's host changed: give the password again", ErrInvalid)
			}

			cfg.Password = old.Password
		case !errors.Is(database.NotFound(err), database.ErrNotFound):
			return nil, fmt.Errorf("get replication %s: %w", cfg.Repository, err)
		}
	}

	if !sealing.SealedString(cfg.Password) {
		sealed, err := s.box.SealString(cfg.Password, PasswordPlace(cfg.Repository))
		if err != nil {
			return nil, err
		}

		cfg.Password = sealed
	}

	doc, err := json.Marshal(cfg) //nolint:gosec // G117: the push credential must be stored; sealed above
	if err != nil {
		return nil, fmt.Errorf("encode replication %s: %w", cfg.Repository, err)
	}

	return doc, nil
}

// Delete removes repository's replication; ErrNotConfigured when it has
// none.
func (s *Service) Delete(ctx context.Context, repository string) error {
	return s.db.WithLock(ctx, replicationLock(repository), func() error {
		n, err := s.db.DeleteReplication(ctx, repository)
		if err != nil {
			return fmt.Errorf("delete replication %s: %w", repository, err)
		}

		if n == 0 {
			return ErrNotConfigured
		}

		return nil
	})
}
