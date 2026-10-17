package maven

import (
	"context"
	"encoding/hex"
	"errors"
	"path"
	"strings"

	"relic/internal/artifacts/repository"
)

var checksumFiles = []string{
	".sha1",
	".sha256",
	".sha512",
	".md5",
	".asc",
}

// publishedChecksums reads the .sha1 file Maven repositories publish next to
// an artifact.
func (s *Service) publishedChecksums(
	ctx context.Context,
	r repository.Repository,
	upstreamURL, p string,
	sent repository.Checksums,
) (repository.Checksums, error) {
	name := path.Base(p)
	if sent != (repository.Checksums{}) || IsMetadata(name) || strings.HasSuffix(p, "/") {
		return repository.Checksums{}, nil
	}

	for _, ext := range checksumFiles {
		if strings.HasSuffix(name, ext) {
			return repository.Checksums{}, nil
		}
	}

	body, err := s.repos.FetchUpstream(ctx, r, upstreamURL+".sha1", "", 1024)
	if errors.Is(err, repository.ErrNotFound) {
		return repository.Checksums{}, nil
	}

	if err != nil {
		return repository.Checksums{}, err
	}

	fields := strings.Fields(string(body))
	if len(fields) == 0 || len(fields[0]) != 40 {
		return repository.Checksums{}, nil
	}

	if _, err := hex.DecodeString(fields[0]); err != nil {
		return repository.Checksums{}, nil //nolint:nilerr // an unreadable .sha1 file checks nothing
	}

	return repository.Checksums{
		SHA1: strings.ToLower(fields[0]),
	}, nil
}
