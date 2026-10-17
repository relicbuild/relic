package repository

import "context"

// Visibility decides which items a caller may see in listings and index
// answers.
type Visibility interface {
	Visible(repoKey, path string) bool
	Restricted(repoKey string) bool
}

type visibilityKey struct{}

// WithVisibility returns a context under which the listings format indexes
// are built from (Children, Subtree, FilesNamed, FilesWithProperty), and
// the format services' remote index answers, leave out items v rejects.
func WithVisibility(ctx context.Context, v Visibility) context.Context {
	return context.WithValue(ctx, visibilityKey{}, v)
}

// Filtered reports whether ctx carries a visibility filter (callers with
// caches shared across users bypass them then).
func Filtered(ctx context.Context) bool {
	_, ok := ctx.Value(visibilityKey{}).(Visibility)
	return ok
}

// Restricted reports whether ctx's filter may hide paths in repoKey.
func Restricted(ctx context.Context, repoKey string) bool {
	v, ok := ctx.Value(visibilityKey{}).(Visibility)
	return ok && v.Restricted(repoKey)
}

// Visible reports whether ctx's filter, if any, lets the caller see path in
// repoKey.
func Visible(ctx context.Context, repoKey, path string) bool {
	v, ok := ctx.Value(visibilityKey{}).(Visibility)
	return !ok || !v.Restricted(repoKey) || v.Visible(repoKey, path)
}

func visibleOnly(ctx context.Context, items []Item) []Item {
	if !Filtered(ctx) {
		return items
	}

	out := items[:0]
	for _, it := range items {
		if Visible(ctx, it.Repository, it.Path()) {
			out = append(out, it)
		}
	}

	return out
}
