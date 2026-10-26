package repository

import (
	"context"
	"fmt"
	"iter"

	"relic/internal/platform/database"
)

const subtreePageSize = 1000

// SubtreeItems yields what Subtree returns, in the same order, reading the
// database a page at a time.
func (s *Service) SubtreeItems(ctx context.Context, item Item) iter.Seq2[Item, error] {
	return func(yield func(Item, error) bool) {
		var after database.Item

		for {
			rows, err := s.subtreePage(ctx, item, after.Path, after.Name)
			if err != nil {
				yield(Item{}, fmt.Errorf("list subtree %s/%s: %w", item.Repository, item.Path(), err))
				return
			}

			for _, it := range visibleOnly(ctx, itemsFromRows(rows)) {
				if !yield(it, nil) {
					return
				}
			}

			if len(rows) < subtreePageSize {
				return
			}

			after = rows[len(rows)-1]
		}
	}
}

func (s *Service) subtreePage(ctx context.Context, item Item, afterPath, afterName string) ([]database.Item, error) {
	if item.Name == RootPath {
		return s.db.ListRepositoryItemsPage(ctx, database.ListRepositoryItemsPageParams{
			RepoKey:   item.Repository,
			AfterPath: afterPath,
			AfterName: afterName,
			Max:       subtreePageSize,
		})
	}

	full := item.Path()

	return s.db.ListSubtreePage(ctx, database.ListSubtreePageParams{
		RepoKey:   item.Repository,
		Path:      item.Parent,
		Name:      item.Name,
		FullPath:  full,
		Prefix:    likePrefix(full),
		AfterPath: afterPath,
		AfterName: afterName,
		Max:       subtreePageSize,
	})
}
