package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"relic/internal/platform/database"
	"relic/internal/platform/objectstore"
)

// CollectGarbage deletes blobs no item has referenced for longer than grace,
// at most batch per round, and returns how many it removed. It marks the
// rows deleting, deletes their objects with no transaction open (each under
// the blob's lock, which a deploy taking the blob back also takes), then
// deletes the rows still marked. A blob whose object cannot be deleted keeps
// its row for the next run.
func (s *Service) CollectGarbage(ctx context.Context, grace time.Duration, batch int32) (int, error) {
	total := 0

	var failed []error

	stuck := map[string]bool{}

	for {
		limit := batch + int32(len(stuck)) //nolint:gosec // one entry per blob that failed this run

		shas, err := s.db.MarkCollectableBlobs(ctx, database.MarkCollectableBlobsParams{
			Cutoff: s.now().Add(-grace),
			Max:    limit,
		})
		if err != nil {
			return total, errors.Join(append(failed, fmt.Errorf("mark blobs: %w", err))...)
		}

		deleted := make([]string, 0, len(shas))
		tried := 0

		for _, sha := range shas {
			if stuck[sha] {
				continue
			}

			tried++

			if err := s.deleteObject(ctx, sha); err != nil {
				failed = append(failed, fmt.Errorf("delete object %s: %w", sha, err))
				stuck[sha] = true

				continue
			}

			deleted = append(deleted, sha)
		}

		var n int64
		if len(deleted) > 0 {
			if n, err = s.db.DeleteDeletingBlobs(ctx, deleted); err != nil {
				return total, errors.Join(append(failed, fmt.Errorf("delete blob rows: %w", err))...)
			}
		}

		total += int(n)

		if len(shas) < int(limit) || tried == 0 {
			return total, errors.Join(failed...)
		}
	}
}

func blobLock(sha256Hex string) string { return "blob:" + sha256Hex }

// deleteObject deletes a blob's object unless a deploy has taken the blob
// back since it was marked.
func (s *Service) deleteObject(ctx context.Context, sha256Hex string) error {
	return s.db.WithLock(ctx, blobLock(sha256Hex), func() error {
		deleting, err := s.db.BlobDeleting(ctx, sha256Hex)
		if database.IsNoRows(err) || (err == nil && !deleting) {
			return nil
		}

		if err != nil {
			return err
		}

		return s.blobs.Delete(ctx, objectstore.Key(sha256Hex))
	})
}

// SweepTemporaryUploads deletes uploads in progress, Docker upload chunks
// and staging files older than objectstore.TemporaryTTL.
func (s *Service) SweepTemporaryUploads(ctx context.Context) (int, error) {
	n, err := objectstore.SweepTemporary(ctx, s.blobs, objectstore.TemporaryTTL)
	if err != nil {
		return n, fmt.Errorf("sweep temporary uploads: %w", err)
	}

	return n, nil
}

const staleFilesPage = 500

// DeleteStaleFiles deletes the files modified more than olderThan ago in
// folders named folder, in repositories of the given formats, and returns
// how many it deleted.
func (s *Service) DeleteStaleFiles(
	ctx context.Context,
	folder string,
	formats []Format,
	olderThan time.Duration,
) (int, error) {
	names := make([]string, 0, len(formats))
	for _, f := range formats {
		names = append(names, string(f))
	}

	deleted := 0

	for {
		rows, err := s.db.ListStaleFilesInFolders(ctx, database.ListStaleFilesInFoldersParams{
			Formats: names,
			Folder:  folder,
			Before:  s.now().Add(-olderThan),
			Max:     staleFilesPage,
		})
		if err != nil {
			return deleted, fmt.Errorf("list stale files: %w", err)
		}

		for _, r := range rows {
			err := s.Delete(ctx, r.RepoKey, JoinPath(r.Path, r.Name))
			if errors.Is(err, ErrNotFound) {
				continue
			}

			if err != nil {
				return deleted, fmt.Errorf("delete %s/%s/%s: %w", r.RepoKey, r.Path, r.Name, err)
			}

			deleted++
		}

		if len(rows) < staleFilesPage {
			return deleted, nil
		}
	}
}
