// Package repository implements repositories and the artifacts stored in them:
// deploy, download, metadata and properties.
package repository

import (
	"errors"
	"path"
	"strings"
)

// ErrInvalidPath is returned for repository paths Relic refuses to store.
var ErrInvalidPath = errors.New("invalid path")

// RootPath is the parent path of items at a repository's root.
const RootPath = "."

// CleanPath normalizes a path relative to a repository root: surrounding and
// repeated slashes are dropped.
func CleanPath(p string) (string, error) {
	if !storableText(p) || strings.Contains(p, `\`) {
		return "", ErrInvalidPath
	}

	segments := strings.Split(p, "/")

	kept := segments[:0]
	for _, s := range segments {
		switch s {
		case "":
			continue
		case ".", "..":
			return "", ErrInvalidPath
		}

		kept = append(kept, s)
	}

	return strings.Join(kept, "/"), nil
}

// SplitPath splits a cleaned, non-root path into its parent (RootPath at the
// root) and name.
func SplitPath(p string) (parent, name string) {
	dir, name := path.Split(p)
	if dir == "" {
		return RootPath, name
	}

	return strings.TrimSuffix(dir, "/"), name
}

// JoinPath is the inverse of SplitPath.
func JoinPath(parent, name string) string {
	if parent == RootPath || parent == "" {
		return name
	}

	return parent + "/" + name
}

func likePrefix(folder string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(folder) + "/%"
}
