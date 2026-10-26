// Package terraform serves Terraform and OpenTofu providers through the
// provider network mirror protocol, which the compatibility API serves at
// <repo>/providers/.
package terraform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	"relic/internal/artifacts/repository"
)

// ErrNotFound is returned for providers, versions and archives a
// repository does not have.
var ErrNotFound = errors.New("terraform provider not found")

// Archive is one platform's package of a provider version.
type Archive struct {
	Filename string
	Hash     string
}

// Service reads providers from repositories.
type Service struct {
	repos         *repository.Service
	moduleFetches repository.Coalescer[repository.Item]
	log           *slog.Logger
}

const versionsCache = "versions.terraform.json"

// NewService returns a Service.
func NewService(repos *repository.Service, log *slog.Logger) *Service {
	repos.RegisterMetadata(func(name string) bool {
		return name == versionsCache
	})

	return &Service{
		repos: repos,
		log:   log,
	}
}

var (
	segment     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	platformRe  = regexp.MustCompile(`^[a-z0-9]+_[a-z0-9]+$`)
	maxMetadata = int64(8 << 20)
)

// Address is a provider's source address.
type Address struct {
	Hostname, Namespace, Type string
}

// ParseAddress validates the three segments of a provider address.
func ParseAddress(hostname, namespace, typ string) (Address, bool) {
	a := Address{
		Hostname:  strings.ToLower(hostname),
		Namespace: strings.ToLower(namespace),
		Type:      strings.ToLower(typ),
	}
	ok := segment.MatchString(a.Hostname) && segment.MatchString(a.Namespace) && segment.MatchString(a.Type)

	return a, ok
}

func (a Address) dir() string { return a.Hostname + "/" + a.Namespace + "/" + a.Type }

// Filename is the conventional archive name of a provider package.
func (a Address) Filename(version, platform string) string {
	return "terraform-provider-" + a.Type + "_" + version + "_" + platform + ".zip"
}

func (a Address) parseFilename(name string) (version, platform string, ok bool) {
	rest, ok := strings.CutPrefix(name, "terraform-provider-"+a.Type+"_")
	if !ok {
		return "", "", false
	}

	rest, ok = strings.CutSuffix(rest, ".zip")
	if !ok {
		return "", "", false
	}

	parts := strings.Split(rest, "_")
	if len(parts) != 3 || parts[0] == "" {
		return "", "", false
	}

	return parts[0], parts[1] + "_" + parts[2], true
}

// Versions lists the provider's versions in r.
func (s *Service) Versions(ctx context.Context, r repository.Repository, a Address) ([]string, error) {
	seen := map[string]bool{}

	var (
		out         []string
		upstreamErr error
	)

	for _, m := range s.members(ctx, r, 0) {
		vs, err := s.memberVersions(ctx, m, a)
		switch {
		case errors.Is(err, ErrNotFound):
			continue
		case errors.Is(err, repository.ErrUpstream):
			upstreamErr = err
			continue
		case err != nil:
			return nil, err
		}

		for _, v := range vs {
			if !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}

	if len(out) == 0 {
		if upstreamErr != nil {
			return nil, upstreamErr
		}

		return nil, ErrNotFound
	}

	sort.Strings(out)

	return out, nil
}

// Archives lists a version's archives by platform ("linux_amd64"), from
// the first member of r that has the version.
func (s *Service) Archives(
	ctx context.Context,
	r repository.Repository,
	a Address,
	version string,
) (map[string]Archive, error) {
	var upstreamErr error

	for _, m := range s.members(ctx, r, 0) {
		archives, err := s.memberArchives(ctx, m, a, version)
		switch {
		case err == nil && len(archives) > 0:
			return archives, nil
		case errors.Is(err, repository.ErrUpstream):
			upstreamErr = err
		case err != nil && !errors.Is(err, ErrNotFound):
			return nil, err
		}
	}

	if upstreamErr != nil {
		return nil, upstreamErr
	}

	return nil, ErrNotFound
}

// Package finds an archive by file name through r's members, fetching it
// from a remote's upstream (and caching it) on a miss.
func (s *Service) Package(
	ctx context.Context,
	r repository.Repository,
	a Address,
	filename string,
) (repository.Item, error) {
	version, platform, ok := a.parseFilename(filename)
	if !ok {
		return repository.Item{}, ErrNotFound
	}

	var upstreamErr error

	for _, m := range s.members(ctx, r, 0) {
		var (
			item repository.Item
			err  error
		)
		if m.Class == "remote" {
			item, err = s.remotePackage(ctx, m, a, version, platform)
		} else {
			item, err = s.repos.Stat(ctx, m.Name, a.dir()+"/"+filename)
			if errors.Is(err, repository.ErrNotFound) {
				err = ErrNotFound
			}
		}

		switch {
		case err == nil && !item.IsFolder():
			return item, nil
		case errors.Is(err, repository.ErrUpstream):
			upstreamErr = err
		case err != nil && !errors.Is(err, ErrNotFound):
			return repository.Item{}, err
		}
	}

	if upstreamErr != nil {
		return repository.Item{}, upstreamErr
	}

	return repository.Item{}, ErrNotFound
}

func (s *Service) memberVersions(ctx context.Context, m repository.Repository, a Address) ([]string, error) {
	if m.Class == "remote" {
		doc, err := s.upstreamVersions(ctx, m, a)
		if err != nil {
			return nil, err
		}

		out := make([]string, 0, len(doc.Versions))
		for _, v := range doc.Versions {
			out = append(out, v.Version)
		}

		return out, nil
	}

	byVersion, err := s.localArchives(ctx, m, a)
	if err != nil {
		return nil, err
	}

	out := make([]string, 0, len(byVersion))
	for v := range byVersion {
		out = append(out, v)
	}

	return out, nil
}

func (s *Service) memberArchives(
	ctx context.Context,
	m repository.Repository,
	a Address,
	version string,
) (map[string]Archive, error) {
	if m.Class != "remote" {
		byVersion, err := s.localArchives(ctx, m, a)
		if err != nil {
			return nil, err
		}

		return byVersion[version], nil
	}

	doc, err := s.upstreamVersions(ctx, m, a)
	if err != nil {
		return nil, err
	}

	var platforms []string

	for _, v := range doc.Versions {
		if v.Version == version {
			for _, p := range v.Platforms {
				platforms = append(platforms, p.OS+"_"+p.Arch)
			}
		}
	}

	if len(platforms) == 0 {
		return nil, ErrNotFound
	}

	out := map[string]Archive{}

	var mu sync.Mutex

	var g errgroup.Group
	g.SetLimit(8)

	for _, p := range platforms {
		if !platformRe.MatchString(p) {
			continue
		}

		g.Go(func() error {
			d, err := s.upstreamDownload(ctx, m, a, version, p)
			if errors.Is(err, ErrNotFound) {
				return nil
			}

			if err != nil {
				return err
			}

			if repository.Visible(ctx, m.Name, a.dir()+"/"+d.Filename) {
				mu.Lock()
				out[p] = Archive{
					Filename: d.Filename,
					Hash:     "zh:" + d.Shasum,
				}
				mu.Unlock()
			}

			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	return out, nil
}

func (s *Service) localArchives(
	ctx context.Context,
	m repository.Repository,
	a Address,
) (map[string]map[string]Archive, error) {
	dir, err := s.repos.Stat(ctx, m.Name, a.dir())
	if errors.Is(err, repository.ErrNotFound) {
		return nil, ErrNotFound
	}

	if err != nil {
		return nil, err
	}

	kids, err := s.repos.Children(ctx, dir)
	if err != nil {
		return nil, err
	}

	out := map[string]map[string]Archive{}

	for _, k := range kids {
		if k.IsFolder() || k.Checksums.SHA256 == "" {
			continue
		}

		version, platform, ok := a.parseFilename(k.Name)
		if !ok {
			continue
		}

		if out[version] == nil {
			out[version] = map[string]Archive{}
		}

		out[version][platform] = Archive{
			Filename: k.Name,
			Hash:     "zh:" + k.Checksums.SHA256,
		}
	}

	if len(out) == 0 {
		return nil, ErrNotFound
	}

	return out, nil
}

type upstreamVersionsDoc struct {
	Versions []struct {
		Version   string `json:"version"`
		Platforms []struct {
			OS   string `json:"os"`
			Arch string `json:"arch"`
		} `json:"platforms"`
	} `json:"versions"`
}

type upstreamDownloadDoc struct {
	Filename    string `json:"filename"`
	DownloadURL string `json:"download_url"`
	Shasum      string `json:"shasum"`
}

func (s *Service) registryBase(m repository.Repository, a Address) (string, bool) {
	u, err := url.Parse(strings.TrimSuffix(s.repos.UpstreamOf(m).URL, "/"))
	if err != nil || !strings.EqualFold(u.Hostname(), a.Hostname) {
		return "", false
	}

	return u.String(), true
}

func (s *Service) upstreamVersions(
	ctx context.Context,
	m repository.Repository,
	a Address,
) (upstreamVersionsDoc, error) {
	var doc upstreamVersionsDoc

	base, ok := s.registryBase(m, a)
	if !ok {
		return doc, ErrNotFound
	}

	err := s.upstreamJSON(
		ctx,
		m,
		base+"/v1/providers/"+a.Namespace+"/"+a.Type+"/versions",
		".terraform/"+a.dir()+"/"+versionsCache,
		&doc,
	)

	return doc, err
}

func (s *Service) upstreamDownload(
	ctx context.Context,
	m repository.Repository,
	a Address,
	version, platform string,
) (upstreamDownloadDoc, error) {
	var doc upstreamDownloadDoc

	base, ok := s.registryBase(m, a)
	if !ok || !platformRe.MatchString(platform) {
		return doc, ErrNotFound
	}

	osName, arch, _ := strings.Cut(platform, "_")

	p := "/v1/providers/" + a.Namespace + "/" + a.Type + "/" + url.PathEscape(version) +
		"/download/" + osName + "/" + arch
	if err := s.upstreamJSON(ctx, m, base+p, ".terraform/"+a.dir()+"/"+version+"/"+platform+".json", &doc); err != nil {
		return doc, err
	}

	if doc.Filename != a.Filename(version, platform) || doc.DownloadURL == "" || len(doc.Shasum) != 64 {
		return doc, fmt.Errorf(
			"%w: unexpected download document for %s %s %s",
			repository.ErrUpstream,
			a.dir(),
			version,
			platform,
		)
	}

	return doc, nil
}

func (s *Service) remotePackage(
	ctx context.Context,
	m repository.Repository,
	a Address,
	version, platform string,
) (repository.Item, error) {
	d, err := s.upstreamDownload(ctx, m, a, version, platform)
	if err != nil {
		return repository.Item{}, err
	}

	item, err := s.repos.ResolveURL(ctx, m, d.DownloadURL, a.dir()+"/"+d.Filename)
	if errors.Is(err, repository.ErrNotFound) {
		return repository.Item{}, ErrNotFound
	}

	if err == nil && !strings.EqualFold(item.Checksums.SHA256, d.Shasum) {
		return repository.Item{}, fmt.Errorf(
			"%w: %s does not match the registry's shasum",
			repository.ErrUpstream,
			d.Filename,
		)
	}

	return item, err
}

func (s *Service) upstreamJSON(ctx context.Context, m repository.Repository, u, cachePath string, v any) error {
	item, err := s.repos.ResolveURL(ctx, m, u, cachePath)
	if errors.Is(err, repository.ErrNotFound) {
		return ErrNotFound
	}

	if err != nil {
		return err
	}

	rc, err := s.repos.Open(ctx, item, nil)
	if err != nil {
		return err
	}

	defer func() {
		_ = rc.Close()
	}()

	if err := json.NewDecoder(io.LimitReader(rc, maxMetadata)).Decode(v); err != nil {
		return fmt.Errorf("%w: %s: %w", repository.ErrUpstream, u, err)
	}

	return nil
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
