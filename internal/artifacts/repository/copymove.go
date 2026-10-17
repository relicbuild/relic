package repository

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"relic/internal/access/auth"
	"relic/internal/platform/database"
)

// ErrSelfCopy is returned when a copy or move targets its own source, a path
// inside it, or a folder above it.
var ErrSelfCopy = errors.New("destination is the source, inside it or above it")

// CopyInput describes a copy or move of an item (and, for folders, its
// contents) to DestPath in DestRepo.
type CopyInput struct {
	Source                Item
	DestinationRepository string
	DestinationPath       string
	Move                  bool
	DryRun                bool
	User                  string
	// RefuseFile, when set, refuses the copy when it returns an error for a
	// file under Source.
	RefuseFile func(ctx context.Context, file Item) error
	// Permits, when set, refuses the copy unless it allows every action the
	// copy takes on a file: reading it (and deleting it, for a move) at the
	// source, writing it at the destination and deleting the file it
	// replaces there.
	Permits func(repoKey, path string, action auth.Action) bool
	// CheckUploadLimit refuses the copy of a file larger than the
	// destination accepts as an upload.
	CheckUploadLimit bool

	// merge puts the copy into what is at the destination, replacing only
	// an item of the other kind; before and then run in the same
	// transaction before and after the copy.
	merge  bool
	before func(q database.Querier) error
	then   func(q database.Querier) error
}

// Copy copies or moves an item.
func (s *Service) Copy(ctx context.Context, in CopyInput) error {
	return s.CopyAll(
		ctx,
		[]CopyInput{
			in,
		},
	)
}

type copyPlan struct {
	in           CopyInput
	destination  Repository
	path         string
	projectQuota int64
	uploadLimit  int64
	files        []string
}

// CopyAll copies or moves several items in one transaction: all of them or none.
func (s *Service) CopyAll(ctx context.Context, inputs []CopyInput) error {
	plans := make([]copyPlan, 0, len(inputs))
	for _, in := range inputs {
		plan, err := s.planCopy(ctx, in)
		if err != nil {
			return err
		}

		plans = append(plans, plan)
	}

	now := s.now()
	dryRun := slices.ContainsFunc(inputs, func(in CopyInput) bool {
		return in.DryRun
	})

	err := s.db.WithTx(ctx, func(q database.Querier) error {
		for i := range plans {
			if err := copyInTx(ctx, q, &plans[i], now); err != nil {
				return err
			}
		}

		if dryRun {
			return errDryRun
		}

		return nil
	})
	if errors.Is(err, errDryRun) {
		return nil
	}

	if err != nil {
		return err
	}

	for _, plan := range plans {
		for _, f := range plan.files {
			it, err := s.Stat(ctx, plan.destination.Name, f)
			if errors.Is(err, ErrNotFound) {
				continue
			}

			if err != nil {
				s.log.WarnContext(
					ctx,
					"skipping the hook of a copied file",
					"repo",
					plan.destination.Name,
					"path",
					f,
					"error",
					err,
				)

				continue
			}

			s.runHook(ctx, it)
		}

		if plan.in.Move {
			s.runHookOf(ctx, s.deleteHooks, plan.in.Source)
		}
	}

	return nil
}

func (s *Service) planCopy(ctx context.Context, in CopyInput) (copyPlan, error) {
	dest := DeployInput{
		Repository: in.DestinationRepository,
		Path:       in.DestinationPath,
	}
	if _, err := s.checkDeployTarget(ctx, &dest); err != nil {
		return copyPlan{}, err
	}

	src := in.Source
	if src.Name == RootPath {
		return copyPlan{}, fmt.Errorf("%w: cannot copy a repository root", ErrInvalidPath)
	}

	if src.Repository == dest.Repository && (dest.Path == src.Path() ||
		strings.HasPrefix(dest.Path, src.Path()+"/") ||
		strings.HasPrefix(src.Path(), dest.Path+"/")) {
		return copyPlan{}, ErrSelfCopy
	}

	destination, err := s.GetRepository(ctx, dest.Repository)
	if err != nil {
		return copyPlan{}, err
	}

	if in.RefuseFile != nil {
		items, err := subtree(ctx, s.db, src)
		if err != nil {
			return copyPlan{}, err
		}

		for _, it := range items {
			if it.IsFolder() {
				continue
			}

			if err := in.RefuseFile(ctx, it); err != nil {
				return copyPlan{}, err
			}
		}
	}

	projectQuota, err := s.quotaOfProject(ctx, destination.Project)
	if err != nil {
		return copyPlan{}, err
	}

	var uploadLimit int64
	if in.CheckUploadLimit {
		if uploadLimit, err = s.UploadLimit(ctx, destination.Name); err != nil {
			return copyPlan{}, err
		}
	}

	return copyPlan{
		in:           in,
		destination:  destination,
		path:         dest.Path,
		projectQuota: projectQuota,
		uploadLimit:  uploadLimit,
	}, nil
}

func copyInTx(ctx context.Context, q database.Querier, plan *copyPlan, now time.Time) error {
	in, src, destination := plan.in, plan.in.Source, plan.destination
	plan.files = plan.files[:0]

	items, err := subtree(ctx, q, src)
	if err != nil {
		return err
	}

	if len(items) == 0 {
		return fmt.Errorf("%w: %s/%s", ErrNotFound, src.Repository, src.Path())
	}

	if in.RefuseFile != nil {
		for _, it := range items {
			if it.IsFolder() {
				continue
			}

			if err := in.RefuseFile(ctx, it); err != nil {
				return err
			}
		}
	}

	if in.before != nil {
		if err := in.before(q); err != nil {
			return err
		}
	}

	for _, it := range items {
		target := plan.path
		if it.Path() != src.Path() {
			target += "/" + strings.TrimPrefix(it.Path(), src.Path()+"/")
		}

		if plan.uploadLimit > 0 && !it.IsFolder() && it.Size > plan.uploadLimit {
			return fmt.Errorf(
				"%w: %s/%s has %d bytes, the limit is %d",
				ErrTooLarge,
				it.Repository,
				it.Path(),
				it.Size,
				plan.uploadLimit,
			)
		}

		if in.merge {
			if err := deleteOtherKind(ctx, q, destination.Name, target, it.IsFolder()); err != nil {
				return err
			}
		}

		if in.Permits != nil && !it.IsFolder() {
			if err := permitFileCopy(ctx, q, in, it, destination.Name, target); err != nil {
				return err
			}
		}

		if err := copyItem(
			ctx,
			q,
			it,
			destination.Name,
			target,
			in.User,
			now,
			destination.Config.Immutable,
			in.Move,
		); err != nil {
			return err
		}

		if !it.IsFolder() {
			plan.files = append(plan.files, target)
		}
	}

	if in.Move {
		deleted, err := deleteItem(ctx, q, src)
		if err != nil {
			return err
		}

		if deleted != len(items) {
			return fmt.Errorf("%w: %s/%s changed during the move", ErrConflict, src.Repository, src.Path())
		}

		if err := checkHolds(ctx, q, src.Repository, src.Path()); err != nil {
			return err
		}
	}

	if err := checkQuota(ctx, q, destination.Name, destination.Config.MaxStorage); err != nil {
		return err
	}

	if err := checkProjectQuota(ctx, q, destination.Project, plan.projectQuota); err != nil {
		return err
	}

	if in.then != nil && !in.DryRun {
		return in.then(q)
	}

	return nil
}

var errDryRun = errors.New("dry run")

func deleteOtherKind(ctx context.Context, q database.Querier, repoKey, target string, folder bool) error {
	parent, name := SplitPath(target)

	existing, err := q.GetItem(ctx, database.GetItemParams{
		RepoKey: repoKey,
		Path:    parent,
		Name:    name,
	})
	if database.IsNoRows(err) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("get %s/%s: %w", repoKey, target, err)
	}

	if (existing.Type == TypeFolder) == folder {
		return nil
	}

	_, err = deleteItem(ctx, q, Item{
		Repository: repoKey,
		Parent:     parent,
		Name:       name,
	})

	return err
}

func permitFileCopy(ctx context.Context, q database.Querier, in CopyInput, file Item, repoKey, target string) error {
	if !in.Permits(file.Repository, file.Path(), auth.ActionRead) {
		return fmt.Errorf(
			"%w: no read permission on everything under %s/%s",
			ErrForbidden,
			in.Source.Repository,
			in.Source.Path(),
		)
	}

	if in.Move && !in.Permits(file.Repository, file.Path(), auth.ActionDelete) {
		return fmt.Errorf("%w: no delete permission on %s/%s", ErrForbidden, file.Repository, file.Path())
	}

	if !in.Permits(repoKey, target, auth.ActionWrite) {
		return fmt.Errorf("%w: no write permission on %s/%s", ErrForbidden, repoKey, target)
	}

	parent, name := SplitPath(target)

	existing, err := q.GetItem(ctx, database.GetItemParams{
		RepoKey: repoKey,
		Path:    parent,
		Name:    name,
	})
	if database.IsNoRows(err) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("get %s/%s: %w", repoKey, target, err)
	}

	if existing.Type == TypeFile && !in.Permits(repoKey, target, auth.ActionDelete) {
		return fmt.Errorf("%w: no delete permission on %s/%s", ErrForbidden, repoKey, target)
	}

	return nil
}

func copyItem(
	ctx context.Context,
	q database.Querier,
	it Item,
	repoKey, target, user string,
	now time.Time,
	immutable, move bool,
) error {
	parent, name := SplitPath(target)
	keepCreated := move

	if it.IsFolder() {
		_, err := q.GetItem(ctx, database.GetItemParams{
			RepoKey: repoKey,
			Path:    parent,
			Name:    name,
		})
		if err != nil && !database.IsNoRows(err) {
			return fmt.Errorf("get %s/%s: %w", repoKey, target, err)
		}

		keepCreated = move && err != nil

		if err := ensureFolders(ctx, q, repoKey, target, user, now); err != nil {
			return err
		}
	} else {
		b := database.Blob{
			Sha256: it.Checksums.SHA256,
			Sha1:   it.Checksums.SHA1,
			Md5:    it.Checksums.MD5,
			Size:   it.Size,
		}
		if _, err := upsertFile(ctx, q, repoKey, parent, name, b, user, now, immutable); err != nil {
			return err
		}
	}

	id, err := q.GetItem(ctx, database.GetItemParams{
		RepoKey: repoKey,
		Path:    parent,
		Name:    name,
	})
	if err != nil {
		return fmt.Errorf("get %s/%s: %w", repoKey, target, err)
	}

	srcRow, err := q.GetItem(ctx, database.GetItemParams{
		RepoKey: it.Repository,
		Path:    it.Parent,
		Name:    it.Name,
	})
	if err != nil {
		return fmt.Errorf("get %s/%s: %w", it.Repository, it.Path(), err)
	}

	if keepCreated {
		if err := q.SetItemCreated(ctx, database.SetItemCreatedParams{
			ID:        id.ID,
			Created:   srcRow.Created,
			CreatedBy: srcRow.CreatedBy,
		}); err != nil {
			return fmt.Errorf("keep the creation of %s/%s: %w", repoKey, target, err)
		}
	}

	rows, err := q.GetItemProperties(ctx, srcRow.ID)
	if err != nil {
		return fmt.Errorf("get properties of %s/%s: %w", it.Repository, it.Path(), err)
	}

	props := Properties{}
	for _, r := range rows {
		props[r.Key] = append(props[r.Key], r.Value)
	}

	return replaceProperties(ctx, q, id.ID, props)
}

func deleteItem(ctx context.Context, q database.Querier, item Item) (int, error) {
	var (
		shas []*string
		err  error
	)
	if item.Name == RootPath {
		shas, err = q.DeleteRepositoryItems(ctx, item.Repository)
	} else {
		full := item.Path()
		shas, err = q.DeleteSubtree(
			ctx,
			database.DeleteSubtreeParams{
				RepoKey:  item.Repository,
				Path:     item.Parent,
				Name:     item.Name,
				FullPath: full,
				Prefix:   likePrefix(full),
			},
		)
	}

	if err != nil {
		return 0, fmt.Errorf("delete %s/%s: %w", item.Repository, item.Path(), err)
	}

	return len(shas), releaseAll(ctx, q, shas)
}
