package repository

import (
	"maps"
	"slices"
	"time"
	"uuid"

	"relic/internal/platform/database"
)

// Item types.
const (
	TypeFile   = "file"
	TypeFolder = "folder"
)

// Checksums of a file's content, lowercase hex.
type Checksums struct {
	SHA1, SHA256, MD5 string
}

// Item is a file or folder in a repository.
type Item struct {
	ID         uuid.UUID
	Repository string
	Parent     string
	Name       string
	Type       string
	Size       int64
	Checksums  Checksums
	Created    time.Time
	CreatedBy  string
	Modified   time.Time
	ModifiedBy string
	Updated    time.Time

	format, class, source string
}

// Path returns the item's path relative to the repository root.
func (i Item) Path() string { return JoinPath(i.Parent, i.Name) }

// IsFolder reports whether the item is a folder.
func (i Item) IsFolder() bool { return i.Type == TypeFolder }

func domainItem(r database.Item) Item {
	deref := func(s *string) string {
		if s == nil {
			return ""
		}

		return *s
	}

	return Item{
		ID:         r.ID,
		Repository: r.RepoKey,
		Parent:     r.Path,
		Name:       r.Name,
		Type:       r.Type,
		Size:       r.Size,
		Checksums: Checksums{
			SHA1:   deref(r.Sha1),
			SHA256: deref(r.Sha256),
			MD5:    deref(r.Md5),
		},
		Created:    r.Created,
		CreatedBy:  r.CreatedBy,
		Modified:   r.Modified,
		ModifiedBy: r.ModifiedBy,
		Updated:    r.Updated,
	}
}

// Properties are multi-valued item properties.
type Properties map[string][]string

// Keys returns the property keys in sorted order.
func (p Properties) Keys() []string { return slices.Sorted(maps.Keys(p)) }

func (p Properties) flatten() (keys, values []string) {
	for _, k := range p.Keys() {
		for _, v := range p[k] {
			keys = append(keys, k)
			values = append(values, v)
		}
	}

	return keys, values
}
