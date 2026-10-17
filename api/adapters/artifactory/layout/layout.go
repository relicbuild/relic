// Package layout is the adapter's storage layout.
package layout

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"relic/internal/artifacts/repository"
	"relic/internal/delivery/buildinfo"
	"relic/internal/delivery/release"
)

// Trash is the trash repository and the properties recorded on trashed
// items.
var Trash = repository.TrashLayout{
	Repository:         "auto-trashcan",
	Time:               "trash.time",
	DeletedBy:          "trash.deletedBy",
	OriginalRepository: "trash.originalRepository",
	OriginalPath:       "trash.originalPath",
}

// BuildInfo is where published build info is kept.
var BuildInfo = buildinfo.Layout{
	DefaultRepository: "artifactory-build-info",
	ProjectSuffix:     "-build-info",
}

// ReleaseBundles is where release bundles are kept, with the manifest in
// each version folder.
var ReleaseBundles = release.Layout{
	DefaultRepository: "release-bundles-v2",
	ProjectSuffix:     "-release-bundles-v2",
	ManifestName:      "release-bundle.json.evd",
}

// CacheSuffix names a remote's cache.
const CacheSuffix = "-cache"

// Repositories finds a repository by name.
type Repositories interface {
	GetRepository(ctx context.Context, name string) (repository.Repository, error)
}

// RemoteOfCache returns the remote whose cache is named name
// ("<remote>-cache"). A real repository with that name wins.
func RemoteOfCache(ctx context.Context, repos Repositories, name string) (string, bool, error) {
	remote, ok := strings.CutSuffix(name, CacheSuffix)
	if !ok || remote == "" {
		return "", false, nil
	}

	_, err := repos.GetRepository(ctx, name)
	if !errors.Is(err, repository.ErrRepositoryNotFound) {
		if err != nil {
			return "", false, fmt.Errorf("get repository %s: %w", name, err)
		}

		return "", false, nil
	}

	r, err := repos.GetRepository(ctx, remote)
	if errors.Is(err, repository.ErrRepositoryNotFound) {
		return "", false, nil
	}

	if err != nil {
		return "", false, fmt.Errorf("get repository %s: %w", remote, err)
	}

	if r.Class != repository.ClassRemote {
		return "", false, nil
	}

	return remote, true, nil
}

// ShownName is the name reported for items stored in name: a remote's items
// belong to its cache.
func ShownName(ctx context.Context, repos Repositories, name string) string {
	if r, err := repos.GetRepository(ctx, name); err == nil && r.Class == repository.ClassRemote {
		return name + CacheSuffix
	}

	return name
}
