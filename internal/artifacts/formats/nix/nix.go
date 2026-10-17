// Package nix implements Nix binary caches: nix copy uploads, substituter
// downloads, remote caches such as cache.nixos.org, and virtual caches.
package nix

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strings"

	"relic/internal/artifacts/repository"
)

// Format is the repository format this package serves.
const Format = repository.FormatNix

// CacheInfo is what GET nix-cache-info answers.
const CacheInfo = "StoreDir: /nix/store\nWantMassQuery: 1\nPriority: 40\n"

const maxNarInfo = 1 << 20

var (
	narinfoRe = regexp.MustCompile(`^([0-9a-df-np-sv-z]{32})\.narinfo$`)
	narRe     = regexp.MustCompile(`^nar/([A-Za-z0-9._+-]+)$`)
)

// ErrInvalid is returned for malformed uploads.
var ErrInvalid = errors.New("invalid nix cache upload")

// Service stores and resolves binary cache files.
type Service struct {
	repos *repository.Service
	log   *slog.Logger
}

// NewService returns a Service.
func NewService(repos *repository.Service, log *slog.Logger) *Service {
	return &Service{
		repos: repos,
		log:   log,
	}
}

func storagePath(p string) string {
	if m := narinfoRe.FindStringSubmatch(p); m != nil {
		return "binary-cache/" + m[1] + "/" + p
	}

	return p
}

// Resolve finds a cache file for r: its own, a remote's upstream (cached),
// or a virtual's members' in order.
func (s *Service) Resolve(ctx context.Context, r repository.Repository, p string) (repository.Item, error) {
	for _, m := range s.members(ctx, r, 0) {
		item, err := s.resolveIn(ctx, m, p)
		if err == nil {
			return item, nil
		}

		if !errors.Is(err, repository.ErrNotFound) {
			return repository.Item{}, err
		}
	}

	return repository.Item{}, repository.ErrNotFound
}

func (s *Service) resolveIn(ctx context.Context, m repository.Repository, p string) (repository.Item, error) {
	if m.Class == "remote" {
		up := strings.TrimRight(s.repos.UpstreamOf(m).URL, "/")
		return s.repos.ResolveURL(ctx, m, up+"/"+p, storagePath(p))
	}

	item, err := s.repos.Stat(ctx, m.Name, storagePath(p))
	if err == nil || !errors.Is(err, repository.ErrNotFound) {
		return item, err
	}

	if nar := narRe.FindStringSubmatch(p); nar != nil {
		found, err := s.repos.FilesNamed(ctx, m.Name, "binary-cache", nar[1])
		if err != nil {
			return repository.Item{}, err
		}

		if len(found) > 0 {
			return found[0], nil
		}
	}

	return repository.Item{}, repository.ErrNotFound
}

// Put stores a nix copy upload in local repository r.
func (s *Service) Put(ctx context.Context, r repository.Repository, p string, body io.Reader, user string) error {
	if p == "nix-cache-info" {
		_, err := io.Copy(io.Discard, io.LimitReader(body, 1<<16))
		return err
	}

	m := narinfoRe.FindStringSubmatch(p)
	if m == nil {
		_, err := s.repos.Deploy(ctx, repository.DeployInput{
			Repository: r.Name,
			Path:       p,
			Body:       body,
			User:       user,
		})

		return err
	}

	info, err := io.ReadAll(io.LimitReader(body, maxNarInfo+1))
	if err != nil {
		return err
	}

	if len(info) > maxNarInfo {
		return fmt.Errorf("%w: narinfo too large", ErrInvalid)
	}

	narURL := field(info, "URL")
	if field(info, "StorePath") == "" || narURL == "" {
		return fmt.Errorf("%w: narinfo without StorePath or URL", ErrInvalid)
	}

	folder := "binary-cache/" + m[1]
	if _, err := s.repos.Deploy(
		ctx,
		repository.DeployInput{
			Repository: r.Name,
			Path:       folder + "/" + p,
			Body:       bytes.NewReader(info),
			User:       user,
		},
	); err != nil {
		return err
	}

	if nar := narRe.FindStringSubmatch(narURL); nar != nil {
		staged, err := s.repos.Stat(ctx, r.Name, narURL)
		if err == nil && !staged.IsFolder() {
			return s.repos.Copy(
				ctx,
				repository.CopyInput{
					Source:                staged,
					DestinationRepository: r.Name,
					DestinationPath:       folder + "/" + nar[1],
					Move:                  true,
					User:                  user,
				},
			)
		}
	}

	return nil
}

func field(info []byte, key string) string {
	sc := bufio.NewScanner(bytes.NewReader(info))
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), key+": "); ok {
			return strings.TrimSpace(v)
		}
	}

	return ""
}

func (s *Service) members(ctx context.Context, r repository.Repository, depth int) []repository.Repository {
	if r.Class != "virtual" {
		return []repository.Repository{
			r,
		}
	}

	if depth >= repository.MaxVirtualDepth {
		return nil
	}

	var out []repository.Repository

	for _, key := range s.repos.Members(r) {
		if m, err := s.repos.GetRepository(ctx, key); err == nil {
			out = append(out, s.members(ctx, m, depth+1)...)
		}
	}

	return out
}
