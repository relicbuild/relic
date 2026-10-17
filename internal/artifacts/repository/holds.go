package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"relic/internal/platform/database"
)

// Hold is a legal hold: the item at Path, with everything under it, cannot
// be deleted, moved or overwritten until the hold is released.
type Hold struct {
	Repository string
	Path       string
	Reason     string
	CreatedBy  string
	Created    time.Time
}

// PutHold places a hold on an existing item, or replaces its reason. A hold
// on the repository's root is stored with an empty path.
func (s *Service) PutHold(ctx context.Context, repoKey, path, reason, user string) error {
	item, err := s.Stat(ctx, repoKey, path)
	if err != nil {
		return err
	}

	held := item.Path()
	if item.Name == RootPath {
		held = ""
	}

	return s.db.WithTx(ctx, func(q database.Querier) error {
		if item.Name != RootPath {
			if _, err := q.GetItemForUpdate(ctx, database.GetItemForUpdateParams{
				RepoKey: repoKey,
				Path:    item.Parent,
				Name:    item.Name,
			}); err != nil {
				if errors.Is(database.NotFound(err), database.ErrNotFound) {
					return fmt.Errorf("%w: %s/%s", ErrNotFound, repoKey, item.Path())
				}

				return fmt.Errorf("lock %s/%s: %w", repoKey, item.Path(), err)
			}
		}

		if err := q.PutHold(ctx, database.PutHoldParams{
			RepoKey:   repoKey,
			Path:      held,
			Reason:    reason,
			CreatedBy: user,
			Created:   s.now(),
		}); err != nil {
			return fmt.Errorf("hold %s/%s: %w", repoKey, held, err)
		}

		return nil
	})
}

// ReleaseHold removes the hold placed on exactly repoKey/path.
func (s *Service) ReleaseHold(ctx context.Context, repoKey, path string) error {
	clean, err := CleanPath(path)
	if err != nil {
		return err
	}

	n, err := s.db.DeleteHold(ctx, database.DeleteHoldParams{
		RepoKey: repoKey,
		Path:    clean,
	})
	if err != nil {
		return fmt.Errorf("release hold on %s/%s: %w", repoKey, clean, err)
	}

	if n == 0 {
		return fmt.Errorf("%w: no hold on %s/%s", ErrNotFound, repoKey, clean)
	}

	return nil
}

// Holds lists every hold.
func (s *Service) Holds(ctx context.Context) ([]Hold, error) {
	rows, err := s.db.ListHolds(ctx)
	if err != nil {
		return nil, fmt.Errorf("list holds: %w", err)
	}

	out := make([]Hold, 0, len(rows))
	for _, r := range rows {
		out = append(out, Hold{
			Repository: r.RepoKey,
			Path:       r.Path,
			Reason:     r.Reason,
			CreatedBy:  r.CreatedBy,
			Created:    r.Created,
		})
	}

	return out, nil
}

// checkHolds fails when a hold covers path (on it, a folder above it or
// anything under it); an empty path is the whole repository.
func checkHolds(ctx context.Context, q database.Querier, repoKey, path string) error {
	holds, err := q.HoldsTouching(ctx, database.HoldsTouchingParams{
		RepoKey: repoKey,
		Path:    path,
	})
	if err != nil {
		return fmt.Errorf("holds on %s/%s: %w", repoKey, path, err)
	}

	if len(holds) > 0 {
		return fmt.Errorf("%w: %s/%s (%s)", ErrHeld, repoKey, holds[0].Path, holds[0].Reason)
	}

	return nil
}
