package replication

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"relic/internal/artifacts/repository"
	"relic/internal/platform/database"
)

// pull copies the target's files the repository lacks or holds an older
// copy of; a federated pull also skips files the repository deleted after
// the target's copy was written.
func (p *Pusher) pull(ctx context.Context, t Target, cfg Config) (Result, error) {
	files, err := t.Files(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("list target: %w", err)
	}

	federated := cfg.Effective() == DirectionFederated

	deleted := map[string]time.Time{}

	if federated {
		rows, err := p.db.ListTombstones(ctx, cfg.Repository)
		if err != nil {
			return Result{}, fmt.Errorf("list tombstones: %w", err)
		}

		for _, r := range rows {
			deleted[r.Path] = r.Deleted
		}
	}

	prefix := strings.Trim(cfg.PathPrefix, "/")
	remotePaths := map[string]bool{}

	var (
		res    Result
		failed []error
	)

	for _, path := range files {
		if !underPrefix(path, prefix) {
			continue
		}

		remotePaths[path] = true

		pulled, err := p.pullFile(ctx, t, cfg, path, deleted, federated)
		if ctx.Err() != nil {
			return res, errors.Join(append(failed, ctx.Err())...)
		}

		switch {
		case err != nil:
			res.Failed++

			if len(failed) < maxReportedFailures {
				failed = append(failed, fmt.Errorf("pull %s: %w", path, err))
			}
		case pulled:
			res.Pulled++
		default:
			res.Skipped++
		}
	}

	if cfg.SyncDeletes && cfg.Effective() == DirectionPull {
		n, err := p.deleteMissing(ctx, cfg.Repository, prefix, remotePaths)
		res.Deleted = n

		if err != nil {
			failed = append(failed, err)
		}
	}

	return res, errors.Join(failed...)
}

func (p *Pusher) pullFile(
	ctx context.Context,
	t Target,
	cfg Config,
	path string,
	deleted map[string]time.Time,
	federated bool,
) (bool, error) {
	remote, err := t.Stat(ctx, path)
	if err != nil || !remote.Found {
		return false, err
	}

	local, err := p.repos.Stat(ctx, cfg.Repository, path)

	switch {
	case err == nil && local.IsFolder():
		return false, nil
	case err == nil && strings.EqualFold(local.Checksums.SHA1, remote.Checksums.SHA1):
		return false, nil
	case err == nil && federated && !remote.Modified.After(local.Modified):
		return false, nil
	case err != nil && !errors.Is(err, repository.ErrNotFound):
		return false, err
	}

	if when, ok := deleted[path]; ok && federated && !remote.Modified.After(when) {
		return false, nil
	}

	body, file, err := t.Open(ctx, path)
	if err != nil {
		return false, err
	}

	defer func() {
		_ = body.Close()
	}()

	if _, err := p.repos.Deploy(ctx, repository.DeployInput{
		Repository: cfg.Repository,
		Path:       path,
		Body:       body,
		Size:       file.Size,
		Expected:   file.Checksums,
		User:       repository.SystemUser,
	}); err != nil {
		return false, err
	}

	return true, nil
}

// deleteMissing deletes the repository's files under prefix the target no
// longer has.
func (p *Pusher) deleteMissing(ctx context.Context, repo, prefix string, remote map[string]bool) (int, error) {
	root, err := p.repos.Stat(ctx, repo, "")
	if err != nil {
		return 0, err
	}

	items, err := p.repos.Subtree(ctx, root)
	if err != nil {
		return 0, err
	}

	n := 0

	for _, it := range items {
		if it.IsFolder() || !underPrefix(it.Path(), prefix) || remote[it.Path()] {
			continue
		}

		if err := p.repos.Delete(ctx, repo, it.Path()); err != nil && !errors.Is(err, repository.ErrNotFound) {
			return n, fmt.Errorf("delete %s: %w", it.Path(), err)
		}

		n++
	}

	return n, nil
}

// pushTombstones deletes on the target the files the repository deleted
// after the target's copy was written.
func (p *Pusher) pushTombstones(ctx context.Context, t Target, cfg Config) (int, error) {
	rows, err := p.db.ListTombstones(ctx, cfg.Repository)
	if err != nil {
		return 0, fmt.Errorf("list tombstones: %w", err)
	}

	var (
		n      int
		failed []error
	)

	for _, r := range rows {
		if !underPrefix(r.Path, strings.Trim(cfg.PathPrefix, "/")) {
			continue
		}

		err := pushTombstone(ctx, t, r)
		if ctx.Err() != nil {
			return n, errors.Join(append(failed, ctx.Err())...)
		}

		switch {
		case errors.Is(err, errNothingToDelete):
		case err != nil:
			if len(failed) < maxReportedFailures {
				failed = append(failed, err)
			}
		default:
			n++
		}
	}

	return n, errors.Join(failed...)
}

var errNothingToDelete = errors.New("the target has no copy older than the deletion")

func pushTombstone(ctx context.Context, t Target, r database.ReplicationTombstone) error {
	remote, err := t.Stat(ctx, r.Path)
	if err != nil {
		return fmt.Errorf("stat %s on target: %w", r.Path, err)
	}

	if !remote.Found || remote.Modified.After(r.Deleted) {
		return errNothingToDelete
	}

	if err := t.Delete(ctx, r.Path); err != nil {
		return fmt.Errorf("delete %s on target: %w", r.Path, err)
	}

	return nil
}

// RecordDelete remembers that path was deleted from a federated repository.
func (p *Pusher) RecordDelete(ctx context.Context, repo, path string, when time.Time) error {
	return p.db.PutTombstone(ctx, database.PutTombstoneParams{
		RepoKey: repo,
		Path:    path,
		Deleted: when.UTC(),
	})
}

// ForgetDelete drops the tombstone of path, deployed again.
func (p *Pusher) ForgetDelete(ctx context.Context, repo, path string) error {
	return p.db.DeleteTombstone(ctx, database.DeleteTombstoneParams{
		RepoKey: repo,
		Path:    path,
	})
}
