package repository

import "context"

// Version is a package version a remote repository's metadata offers, with
// the path its file has in the repository when it is cached.
type Version struct {
	Version string
	Path    string
}

// VersionGate returns the versions of a package that clients must not be
// offered: known malicious, quarantined, or blocked by a stored finding.
// ecosystem is the package URL type (npm, pypi).
type VersionGate func(ctx context.Context, r Repository, ecosystem, name string, versions []Version) (map[string]bool, error)

// SetVersionGate makes package metadata leave out the versions gate refuses.
func (s *Service) SetVersionGate(gate VersionGate) { s.versionGate = gate }

// RefusedVersions are the versions of a package the version gate refuses;
// none without a gate.
func (s *Service) RefusedVersions(
	ctx context.Context,
	r Repository,
	ecosystem, name string,
	versions []Version,
) (map[string]bool, error) {
	if s.versionGate == nil || len(versions) == 0 {
		return map[string]bool{}, nil
	}

	return s.versionGate(ctx, r, ecosystem, name, versions)
}
