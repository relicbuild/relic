package repository

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"relic/internal/platform/database"
)

// TrashLayout names the trash repository and the properties recorded on the
// root of each trashed item.
type TrashLayout struct {
	Repository         string
	Time               string
	DeletedBy          string
	OriginalRepository string
	OriginalPath       string
}

// DefaultTrashLayout is Relic's own trash layout.
var DefaultTrashLayout = TrashLayout{
	Repository:         "relic-trash",
	Time:               "trash.time",
	DeletedBy:          "trash.deleted-by",
	OriginalRepository: "trash.original-repository",
	OriginalPath:       "trash.original-path",
}

// SetTrashLayout sets the trash repository and property names.
func (s *Service) SetTrashLayout(l TrashLayout) { s.trashLayout = l }

// TrashRepository is the name of the trash repository.
func (s *Service) TrashRepository() string { return s.trashLayout.Repository }

func (l TrashLayout) keys() []string {
	return []string{
		l.Time,
		l.DeletedBy,
		l.OriginalRepository,
		l.OriginalPath,
	}
}

// SetTrash enables the trash can for DeleteToTrash (on by default).
func (s *Service) SetTrash(enabled bool) { s.trashDisabled = !enabled }

// DeleteToTrash deletes an item the way a user delete does: into the trash
// can when it is enabled, permanently otherwise and for items already in
// the trash.
func (s *Service) DeleteToTrash(ctx context.Context, repoKey, path, user string) error {
	return s.DeleteToTrashChecked(ctx, repoKey, path, user, nil)
}

// DeleteToTrashChecked is DeleteToTrash refusing the whole delete when
// refuseFile returns an error for a file it would delete, checked in the
// transaction that moves them.
func (s *Service) DeleteToTrashChecked(
	ctx context.Context,
	repoKey, path, user string,
	refuseFile func(ctx context.Context, file Item) error,
) error {
	if refuseFile != nil {
		item, err := s.Stat(ctx, repoKey, path)
		if err != nil {
			return err
		}

		items, err := s.Subtree(ctx, item)
		if err != nil {
			return err
		}

		for _, it := range items {
			if it.IsFolder() {
				continue
			}

			if err := refuseFile(ctx, it); err != nil {
				return err
			}
		}
	}

	if s.trashDisabled || repoKey == s.trashLayout.Repository {
		return s.Delete(ctx, repoKey, path)
	}

	item, err := s.Stat(ctx, repoKey, path)
	if err != nil {
		return err
	}

	if item.Name == RootPath {
		if err := checkHolds(ctx, s.db, item.Repository, ""); err != nil {
			return err
		}

		kids, err := s.Children(ctx, item)
		if err != nil {
			return err
		}

		for _, k := range kids {
			if err := s.trash(ctx, k, user, refuseFile); err != nil {
				return err
			}
		}

		return nil
	}

	return s.trash(ctx, item, user, refuseFile)
}

func (s *Service) trash(
	ctx context.Context,
	item Item,
	user string,
	refuseFile func(context.Context, Item) error,
) error {
	if err := s.ensureTrashRepo(ctx); err != nil {
		return err
	}

	dest := item.Repository + "/" + item.Path()
	parent, name := SplitPath(dest)
	props := Properties{
		s.trashLayout.Time: {
			strconv.FormatInt(s.now().UnixMilli(), 10),
		},
		s.trashLayout.DeletedBy: {
			user,
		},
		s.trashLayout.OriginalRepository: {
			item.Repository,
		},
		s.trashLayout.OriginalPath: {
			item.Path(),
		},
	}

	err := s.Copy(
		ctx,
		CopyInput{
			Source:                item,
			DestinationRepository: s.trashLayout.Repository,
			DestinationPath:       dest,
			Move:                  true,
			User:                  user,
			RefuseFile:            refuseFile,
			merge:                 true,
			before: func(q database.Querier) error {
				return s.clearEarlierEntry(ctx, q, dest)
			},
			then: func(q database.Querier) error {
				trashed, err := q.GetItem(ctx, database.GetItemParams{
					RepoKey: s.trashLayout.Repository,
					Path:    parent,
					Name:    name,
				})
				if err != nil {
					return fmt.Errorf("find trash entry %s: %w", dest, err)
				}

				return setProperties(ctx, q, trashed.ID, props)
			},
		},
	)
	if err != nil {
		return fmt.Errorf("move %s/%s to the trash: %w", item.Repository, item.Path(), err)
	}

	s.publishDeleted(ctx, item)

	return nil
}

// clearEarlierEntry deletes what an earlier trash entry at path holds,
// except the entries trashed on their own below it.
func (s *Service) clearEarlierEntry(ctx context.Context, q database.Querier, path string) error {
	parent, name := SplitPath(path)

	_, err := q.GetItem(ctx, database.GetItemParams{
		RepoKey: s.trashLayout.Repository,
		Path:    parent,
		Name:    name,
	})
	if database.IsNoRows(err) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("get trash item %s: %w", path, err)
	}

	rows, err := q.ListItemsWithProperty(
		ctx,
		database.ListItemsWithPropertyParams{
			RepoKey: s.trashLayout.Repository,
			Key:     s.trashLayout.Time,
		},
	)
	if err != nil {
		return fmt.Errorf("list trash: %w", err)
	}

	var (
		isEntry bool
		nested  []string
	)

	for _, r := range rows {
		switch entry := domainItem(r.Item).Path(); {
		case entry == path:
			isEntry = true
		case strings.HasPrefix(entry, path+"/"):
			nested = append(nested, entry)
		}
	}

	if !isEntry {
		return nil
	}

	held, err := q.ListSubtree(
		ctx,
		database.ListSubtreeParams{
			RepoKey:  s.trashLayout.Repository,
			Path:     parent,
			Name:     name,
			FullPath: path,
			Prefix:   likePrefix(path),
		},
	)
	if err != nil {
		return fmt.Errorf("list trash subtree %s: %w", path, err)
	}

	var deleted []string

	for _, it := range itemsFromRows(held) {
		p := it.Path()
		if p == path || withinAny(p, deleted) || withinAny(p, nested) || holdsAny(p, nested) {
			continue
		}

		if _, err := deleteItem(ctx, q, it); err != nil {
			return err
		}

		deleted = append(deleted, p)
	}

	return nil
}

func withinAny(path string, folders []string) bool {
	return slices.ContainsFunc(folders, func(folder string) bool {
		return path == folder || strings.HasPrefix(path, folder+"/")
	})
}

func holdsAny(folder string, paths []string) bool {
	return slices.ContainsFunc(paths, func(p string) bool {
		return strings.HasPrefix(p, folder+"/")
	})
}

// TrashEntry is one deleted item in the trash can.
type TrashEntry struct {
	Item               Item
	Deleted            time.Time
	DeletedBy          string
	OriginalRepository string
	OriginalPath       string
}

// TrashQuery selects trash can entries: Query matches the original place
// and the deleter, Kind is TypeFile or TypeFolder, and empty filters select
// everything.
type TrashQuery struct {
	Query              string
	OriginalRepository string
	DeletedBy          string
	Kind               string
	Skip               int
	Max                int
}

// TrashEntries lists a page of the trash can's entries, newest first, and
// counts the entries the query selects.
func (s *Service) TrashEntries(ctx context.Context, q TrashQuery) ([]TrashEntry, int64, error) {
	total, err := s.db.CountTrashEntries(
		ctx,
		database.CountTrashEntriesParams{
			TimeKey:               s.trashLayout.Time,
			RepoKey:               s.trashLayout.Repository,
			Kind:                  q.Kind,
			DeletedBy:             q.DeletedBy,
			DeletedByKey:          s.trashLayout.DeletedBy,
			OriginalRepository:    q.OriginalRepository,
			OriginalRepositoryKey: s.trashLayout.OriginalRepository,
			Query:                 q.Query,
		},
	)
	if err != nil {
		return nil, 0, fmt.Errorf("count trash: %w", err)
	}

	rows, err := s.db.ListTrashEntries(
		ctx,
		database.ListTrashEntriesParams{
			DeletedByKey:          s.trashLayout.DeletedBy,
			OriginalRepositoryKey: s.trashLayout.OriginalRepository,
			OriginalPathKey:       s.trashLayout.OriginalPath,
			TimeKey:               s.trashLayout.Time,
			RepoKey:               s.trashLayout.Repository,
			Kind:                  q.Kind,
			DeletedBy:             q.DeletedBy,
			OriginalRepository:    q.OriginalRepository,
			Query:                 q.Query,
			Skip:                  int32(min(q.Skip, math.MaxInt32)), //nolint:gosec // clamped
			MaxRows:               int32(min(q.Max, math.MaxInt32)),  //nolint:gosec // clamped
		},
	)
	if err != nil {
		return nil, 0, fmt.Errorf("list trash: %w", err)
	}

	out := make([]TrashEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, TrashEntry{
			Item:               domainItem(r.Item),
			Deleted:            trashTime(r.Deleted),
			DeletedBy:          r.DeletedBy,
			OriginalRepository: r.OriginalRepository,
			OriginalPath:       r.OriginalPath,
		})
	}

	return out, total, nil
}

// TrashOriginalRepositories names every repository with entries in the
// trash can.
func (s *Service) TrashOriginalRepositories(ctx context.Context) ([]string, error) {
	return s.trashPropertyValues(ctx, s.trashLayout.OriginalRepository)
}

// TrashDeleters names every user with entries in the trash can.
func (s *Service) TrashDeleters(ctx context.Context) ([]string, error) {
	return s.trashPropertyValues(ctx, s.trashLayout.DeletedBy)
}

func (s *Service) trashPropertyValues(ctx context.Context, key string) ([]string, error) {
	values, err := s.db.ListTrashPropertyValues(
		ctx,
		database.ListTrashPropertyValuesParams{
			TimeKey: s.trashLayout.Time,
			Key:     key,
			RepoKey: s.trashLayout.Repository,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("list trash property %s: %w", key, err)
	}

	return values, nil
}

func trashTime(milliseconds string) time.Time {
	ms, _ := strconv.ParseInt(milliseconds, 10, 64)

	return time.UnixMilli(ms).UTC()
}

// ErrNoRestoreTarget is returned when a trash entry has no recorded origin
// and no destination was given.
var ErrNoRestoreTarget = errors.New("no restore destination")

// RestoreFromTrash moves a trash entry (a path inside the trash can) back
// to toRepo/toPath, or to where it was deleted from when those are empty.
func (s *Service) RestoreFromTrash(ctx context.Context, trashPath, toRepo, toPath, user string) error {
	item, err := s.Stat(ctx, s.trashLayout.Repository, trashPath)
	if err != nil {
		return err
	}

	if toRepo == "" {
		props, err := s.Properties(ctx, item)
		if err != nil {
			return err
		}

		toRepo, toPath = first(props[s.trashLayout.OriginalRepository]), first(props[s.trashLayout.OriginalPath])
		if toRepo == "" {
			toRepo, toPath, _ = strings.Cut(item.Path(), "/")
		}
	}

	if toRepo == "" || toPath == "" {
		return ErrNoRestoreTarget
	}

	if err := s.Copy(
		ctx,
		CopyInput{
			Source:                item,
			DestinationRepository: toRepo,
			DestinationPath:       toPath,
			Move:                  true,
			User:                  user,
		},
	); err != nil {
		return err
	}

	restored, err := s.Stat(ctx, toRepo, toPath)
	if err != nil {
		return err
	}

	return s.DeleteProperties(ctx, restored, s.trashLayout.keys(), true)
}

// EmptyTrash deletes everything in the trash can.
func (s *Service) EmptyTrash(ctx context.Context) error {
	err := s.Delete(ctx, s.trashLayout.Repository, "")
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrRepositoryNotFound) {
		return nil
	}

	return err
}

// PurgeTrash deletes trash entries deleted before cutoff and returns how
// many were removed.
func (s *Service) PurgeTrash(ctx context.Context, cutoff time.Time) (int, error) {
	rows, err := s.db.ListItemsWithProperty(
		ctx,
		database.ListItemsWithPropertyParams{
			RepoKey: s.trashLayout.Repository,
			Key:     s.trashLayout.Time,
		},
	)
	if err != nil {
		return 0, fmt.Errorf("list trash: %w", err)
	}

	var (
		expired []Item
		kept    []string
	)

	for _, r := range rows {
		item := domainItem(r.Item)
		if trashTime(r.PropertyValue).Before(cutoff) {
			expired = append(expired, item)
		} else {
			kept = append(kept, item.Path())
		}
	}

	n := 0

	for _, item := range expired {
		if err := s.purgeTrashEntry(ctx, item, kept); err != nil && !errors.Is(err, ErrNotFound) {
			return n, err
		}

		n++
	}

	return n, nil
}

func (s *Service) purgeTrashEntry(ctx context.Context, entry Item, kept []string) error {
	nested := slices.ContainsFunc(kept, func(p string) bool {
		return strings.HasPrefix(p, entry.Path()+"/")
	})
	if !nested {
		return s.Delete(ctx, s.trashLayout.Repository, entry.Path())
	}

	if err := s.DeleteProperties(ctx, entry, s.trashLayout.keys(), false); err != nil {
		return err
	}

	return s.deleteAllBut(ctx, entry, kept)
}

func (s *Service) deleteAllBut(ctx context.Context, folder Item, kept []string) error {
	kids, err := s.Children(ctx, folder)
	if err != nil {
		return err
	}

	for _, k := range kids {
		p := k.Path()

		switch {
		case slices.Contains(kept, p):
		case slices.ContainsFunc(kept, func(keptPath string) bool {
			return strings.HasPrefix(keptPath, p+"/")
		}):
			if err := s.deleteAllBut(ctx, k, kept); err != nil {
				return err
			}
		default:
			if err := s.Delete(ctx, k.Repository, p); err != nil && !errors.Is(err, ErrNotFound) {
				return err
			}
		}
	}

	return nil
}

func (s *Service) ensureTrashRepo(ctx context.Context) error {
	if _, err := s.GetRepository(ctx, s.trashLayout.Repository); err == nil {
		return nil
	}

	r := Repository{
		Name:   s.trashLayout.Repository,
		Class:  ClassLocal,
		Format: FormatGeneric,
		Config: Config{
			Description: "Trash can",
		},
	}
	if err := s.CreateRepository(ctx, r); err != nil && !errors.Is(err, ErrRepositoryExists) {
		return err
	}

	return nil
}

func first(v []string) string {
	if len(v) == 0 {
		return ""
	}

	return v[0]
}
