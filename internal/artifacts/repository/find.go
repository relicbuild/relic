package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"relic/internal/platform/database"
)

// FindBySHA256 returns files in repoKey with the given content at or below
// folder ("" for the whole repository).
func (s *Service) FindBySHA256(ctx context.Context, repoKey, folder, sha256Hex string) ([]Item, error) {
	rows, err := s.db.FindFilesBySha256(ctx, database.FindFilesBySha256Params{
		RepoKey: repoKey,
		Sha256:  &sha256Hex,
		Prefix:  folderPrefix(folder),
	})
	if err != nil {
		return nil, fmt.Errorf("find %s in %s: %w", sha256Hex, repoKey, err)
	}

	return visibleOnly(ctx, below(itemsFromRows(rows), folder)), nil
}

// FilesNamed returns files in repoKey named one of names at or below folder
// ("" for the whole repository).
func (s *Service) FilesNamed(ctx context.Context, repoKey, folder string, names ...string) ([]Item, error) {
	rows, err := s.db.FindFilesNamed(ctx, database.FindFilesNamedParams{
		RepoKey: repoKey,
		Names:   names,
		Prefix:  folderPrefix(folder),
	})
	if err != nil {
		return nil, fmt.Errorf("find %v in %s: %w", names, repoKey, err)
	}

	return visibleOnly(ctx, below(itemsFromRows(rows), folder)), nil
}

func folderPrefix(folder string) string {
	if folder == "" {
		return "%"
	}

	return strings.TrimSuffix(likePrefix(folder), "/%") + "%"
}

func below(items []Item, folder string) []Item {
	if folder == "" {
		return items
	}

	out := items[:0]
	for _, it := range items {
		if it.Parent == folder || strings.HasPrefix(it.Parent, folder+"/") {
			out = append(out, it)
		}
	}

	return out
}

// FilesWithProperty returns files in repoKey whose property key has value
// (compared case-insensitively).
func (s *Service) FilesWithProperty(ctx context.Context, repoKey, key, value string) ([]Item, error) {
	rows, err := s.db.FindFilesByProperty(
		ctx,
		database.FindFilesByPropertyParams{
			RepoKey: repoKey,
			Key:     key,
			Value:   value,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("find %s=%s in %s: %w", key, value, repoKey, err)
	}

	return visibleOnly(ctx, itemsFromRows(rows)), nil
}

// PropertyFile is a file and the value of one of its properties.
type PropertyFile struct {
	Item  Item
	Value string
}

// FilesWithPropertyKey returns the files in repoKey that have property key,
// each with that property's value, in one query.
func (s *Service) FilesWithPropertyKey(ctx context.Context, repoKey, key string) ([]PropertyFile, error) {
	rows, err := s.db.ListItemsWithProperty(ctx, database.ListItemsWithPropertyParams{
		RepoKey: repoKey,
		Key:     key,
	})
	if err != nil {
		return nil, fmt.Errorf("list %s in %s: %w", key, repoKey, err)
	}

	filtered := Filtered(ctx)
	out := make([]PropertyFile, 0, len(rows))

	for _, r := range rows {
		item := domainItem(r.Item)
		if item.Type != TypeFile || filtered && !Visible(ctx, item.Repository, item.Path()) {
			continue
		}

		out = append(out, PropertyFile{
			Item:  item,
			Value: r.PropertyValue,
		})
	}

	return out, nil
}

// FilesWithContent returns up to 100 files, in any repository, whose
// content has the SHA-256 (preferred) or SHA-1 in sums.
func (s *Service) FilesWithContent(ctx context.Context, sums Checksums) ([]Item, error) {
	rows, err := s.db.FindFilesByContent(
		ctx,
		database.FindFilesByContentParams{
			Sha256: strings.ToLower(sums.SHA256),
			Sha1:   strings.ToLower(sums.SHA1),
		},
	)
	if err != nil {
		return nil, fmt.Errorf("find files by content: %w", err)
	}

	return itemsFromRows(rows), nil
}

// SearchNames returns up to limit items of repoKey whose name contains
// term, case-insensitively.
func (s *Service) SearchNames(ctx context.Context, repoKey, term string, limit int32) ([]Item, error) {
	esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(term)

	rows, err := s.db.SearchItemsByName(
		ctx,
		database.SearchItemsByNameParams{
			RepoKey: repoKey,
			Pattern: "%" + esc + "%",
			MaxRows: limit,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("search %s: %w", repoKey, err)
	}

	return itemsFromRows(rows), nil
}

// SHA256OfSHA1 returns the SHA-256 of stored content with the given SHA-1,
// empty when nothing stored has it, for clients (build info) that name
// content by SHA-1 only.
func (s *Service) SHA256OfSHA1(ctx context.Context, sha1 string) (string, error) {
	b, err := s.db.GetBlobBySHA1(ctx, strings.ToLower(sha1))
	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		return "", nil
	}

	if err != nil {
		return "", fmt.Errorf("blob with sha1 %s: %w", sha1, err)
	}

	return b.Sha256, nil
}
