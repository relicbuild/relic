// Package gomod implements the Go module proxy protocol (GOPROXY) on Relic
// repositories.
package gomod

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"relic/internal/artifacts/repository"
	"relic/internal/platform/telemetry"

	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

// Format is the repository format this package serves.
const Format = repository.FormatGoModules

const maxListBytes = 4 << 20

// ErrNotFound is returned when a module or version is unknown.
var ErrNotFound = errors.New("module or version not found")

// Info is a version's .info document.
type Info struct {
	Version string
	Time    time.Time
}

// Service implements the proxy protocol on top of repo.Service.
type Service struct {
	repos *repository.Service
	log   *slog.Logger
}

// NewService returns a Service.
func NewService(repos *repository.Service, log *slog.Logger) *Service {
	repos.RegisterMetadata(func(name string) bool {
		return name == "list" || name == "@latest"
	})

	return &Service{
		repos: repos,
		log:   log,
	}
}

// ValidModule reports whether escaped is a case-encoded module path.
func ValidModule(escaped string) bool {
	p, err := module.UnescapePath(escaped)
	return err == nil && module.CheckPath(p) == nil
}

// ValidVersion reports whether v is a canonical module version.
func ValidVersion(v string) bool {
	return semver.IsValid(v) && semver.Canonical(v) == strings.TrimSuffix(v, "+incompatible")
}

func pathIn(r repository.Repository, mod string) string {
	if r.Class == "remote" {
		return mod
	}

	if p, err := module.UnescapePath(mod); err == nil {
		return p
	}

	return mod
}

// List returns the module's versions, sorted by semver.
func (s *Service) List(ctx context.Context, r repository.Repository, mod string) ([]string, error) {
	var (
		versions    []string
		upstreamErr error
	)

	found := false

	for _, m := range s.repos.Flatten(ctx, r, mod) {
		vs, err := s.listOne(ctx, m, mod)
		switch {
		case err == nil:
			found = true
		case errors.Is(err, repository.ErrUpstream):
			upstreamErr = err
		case !errors.Is(err, ErrNotFound):
			return nil, err
		}

		versions = append(versions, vs...)
	}

	switch {
	case len(versions) == 0 && upstreamErr != nil:
		return nil, upstreamErr
	case !found:
		return nil, ErrNotFound
	}

	semver.Sort(versions)

	return slices.Compact(versions), nil
}

func (s *Service) listOne(ctx context.Context, r repository.Repository, mod string) ([]string, error) {
	if r.Class == "remote" {
		item, err := s.repos.Resolve(ctx, r.Name, pathIn(r, mod)+"/@v/list")
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrNotFound
		}

		if err != nil {
			return nil, err
		}

		body, err := s.read(ctx, item, maxListBytes)
		if err != nil {
			return nil, err
		}

		var out []string

		restricted := repository.Restricted(ctx, r.Name)
		for v := range strings.FieldsSeq(string(body)) {
			if ValidVersion(v) && (!restricted || repository.Visible(ctx, r.Name, pathIn(r, mod)+"/@v/"+v+".zip")) {
				out = append(out, v)
			}
		}

		return out, nil
	}

	dir, err := s.repos.Stat(ctx, r.Name, pathIn(r, mod)+"/@v")
	if errors.Is(err, repository.ErrNotFound) {
		return nil, ErrNotFound
	}

	if err != nil {
		return nil, err
	}

	children, err := s.repos.Children(ctx, dir)
	if err != nil {
		return nil, err
	}

	var out []string

	for _, c := range children {
		if v, ok := strings.CutSuffix(c.Name, ".zip"); ok && ValidVersion(v) {
			out = append(out, v)
		}
	}

	return out, nil
}

// Latest returns the newest version: the highest release, else the highest
// pre-release.
func (s *Service) Latest(ctx context.Context, r repository.Repository, mod string) (Info, error) {
	versions, err := s.List(ctx, r, mod)
	if err != nil {
		return Info{}, err
	}

	if len(versions) == 0 {
		var upstreamErr error

		for _, m := range s.repos.Flatten(ctx, r, mod) {
			if m.Class != "remote" {
				continue
			}

			item, err := s.repos.Resolve(ctx, m.Name, pathIn(m, mod)+"/@latest")
			switch {
			case err == nil:
				return s.decodeInfo(ctx, item)
			case errors.Is(err, repository.ErrUpstream):
				upstreamErr = err
			case !errors.Is(err, repository.ErrNotFound):
				return Info{}, err
			}
		}

		if upstreamErr != nil {
			return Info{}, upstreamErr
		}

		return Info{}, ErrNotFound
	}

	latest := versions[len(versions)-1]
	for _, version := range slices.Backward(versions) {
		if semver.Prerelease(version) == "" {
			latest = version
			break
		}
	}

	return s.Info(ctx, r, mod, latest)
}

// Info returns a version's .info document, synthesized from the stored
// .zip's creation time when no .info was deployed.
func (s *Service) Info(ctx context.Context, r repository.Repository, mod, version string) (Info, error) {
	item, err := s.File(ctx, r, mod, version+".info")
	if err == nil {
		return s.decodeInfo(ctx, item)
	}

	if !errors.Is(err, ErrNotFound) {
		return Info{}, err
	}

	zip, err := s.File(ctx, r, mod, version+".zip")
	if err != nil {
		return Info{}, err
	}

	return Info{
		Version: version,
		Time:    zip.Created.UTC(),
	}, nil
}

// File finds <mod>/@v/<name> through r's members.
func (s *Service) File(ctx context.Context, r repository.Repository, mod, name string) (repository.Item, error) {
	var upstreamErr error

	for _, m := range s.repos.Flatten(ctx, r, mod) {
		item, err := s.repos.Resolve(ctx, m.Name, pathIn(m, mod)+"/@v/"+name)
		switch {
		case err == nil:
			return item, nil
		case errors.Is(err, repository.ErrUpstream):
			upstreamErr = err
		case !errors.Is(err, repository.ErrNotFound):
			return repository.Item{}, err
		}
	}

	if upstreamErr != nil {
		return repository.Item{}, upstreamErr
	}

	return repository.Item{}, ErrNotFound
}

func (s *Service) decodeInfo(ctx context.Context, item repository.Item) (Info, error) {
	body, err := s.read(ctx, item, 1<<20)
	if err != nil {
		return Info{}, err
	}

	var info Info
	if err := json.Unmarshal(body, &info); err != nil || info.Version == "" {
		invalid := fmt.Errorf("invalid .info at %s/%s", item.Repository, item.Path())

		r, err := s.repos.GetRepository(ctx, item.Repository)
		if err == nil && r.Class == repository.ClassRemote {
			return Info{}, fmt.Errorf("%w: %w", repository.ErrUpstream, invalid)
		}

		return Info{}, invalid
	}

	return info, nil
}

func (s *Service) read(ctx context.Context, item repository.Item, limit int64) ([]byte, error) {
	rc, err := s.repos.Open(ctx, item, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rc.Close()
	}()

	return io.ReadAll(io.LimitReader(rc, limit))
}

var sumDBs = map[string]bool{
	"sum.golang.org":       true,
	"sum.golang.google.cn": true,
}

var sumDBPath = regexp.MustCompile(
	`^(latest|lookup/[A-Za-z0-9._~!/+-]+@[A-Za-z0-9._+-]+|tile/[0-9]+/(data|[0-9]+)/(x[0-9]{3}/)*[0-9]{3}(\.p/[0-9]+)?)$`,
)

func validSumDBPath(p string) bool {
	return sumDBPath.MatchString(p) && !strings.Contains(p, "..")
}

// SumDBRemote returns the remote Go repository r proxies checksum database
// name through: r itself or its first remote member.
func (s *Service) SumDBRemote(ctx context.Context, r repository.Repository, name string) (repository.Repository, bool) {
	if !sumDBs[name] {
		return repository.Repository{}, false
	}

	for _, m := range s.repos.Flatten(ctx, r, "") {
		if m.Class == "remote" {
			return m, true
		}
	}

	return repository.Repository{}, false
}

// SumDB fetches p (latest, lookup/… or tile/…) of checksum database name
// through remote m.
func (s *Service) SumDB(ctx context.Context, m repository.Repository, name, p string) (repository.Item, []byte, error) {
	if !sumDBs[name] || !validSumDBPath(p) {
		return repository.Item{}, nil, ErrNotFound
	}

	u := "https://" + name + "/" + p
	if p != "latest" {
		item, err := s.repos.ResolveURL(ctx, m, u, ".sumdb/"+name+"/"+p)
		if errors.Is(err, repository.ErrNotFound) {
			err = ErrNotFound
		}

		return item, nil, err
	}

	if s.repos.UpstreamOf(m).Offline {
		return repository.Item{}, nil, ErrNotFound
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return repository.Item{}, nil, err
	}

	req.Header.Set("User-Agent", repository.UserAgent)

	resp, err := s.repos.UpstreamClient().Do(req.WithContext(telemetry.WithUpstream(ctx, m.Name, string(m.Format))))
	if err != nil {
		return repository.Item{}, nil, fmt.Errorf("%w: %w", repository.ErrUpstream, err)
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return repository.Item{}, nil, fmt.Errorf("%w: %s returned %s", repository.ErrUpstream, u, resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return repository.Item{}, nil, fmt.Errorf("%w: %w", repository.ErrUpstream, err)
	}

	return repository.Item{}, body, nil
}
