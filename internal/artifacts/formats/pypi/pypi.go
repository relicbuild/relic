// Package pypi implements the Python package index protocols on Relic
// repositories: the legacy upload API used by twine, and
// the PEP 503 simple index used by pip, for local, remote and virtual
// repositories.
package pypi

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"relic/internal/artifacts/repository"
)

// Format is the repository format this package serves.
const Format = repository.FormatPyPI

// Errors mapped by the API layer.
var (
	ErrNotFound = errors.New("project not found")
	ErrInvalid  = errors.New("invalid distribution")
)

var (
	normalizeRe = regexp.MustCompile(`[-_.]+`)
	nameRe      = regexp.MustCompile(`^([A-Za-z0-9]|[A-Za-z0-9][A-Za-z0-9._-]*[A-Za-z0-9])$`)
	versionRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+!_-]*$`)
	anchorRe    = regexp.MustCompile(`(?is)<a\s+([^>]*)>(.*?)</a>`)
	attrRe      = regexp.MustCompile(`(?s)([a-zA-Z][a-zA-Z0-9-]*)\s*=\s*"([^"]*)"`)
)

// Normalize is PEP 503 name normalization.
func Normalize(name string) string { return strings.ToLower(normalizeRe.ReplaceAllString(name, "-")) }

// ValidName reports whether name is a valid project name (PEP 508).
func ValidName(name string) bool { return len(name) <= 200 && nameRe.MatchString(name) }

var distExts = []string{
	".whl",
	".tar.gz",
	".zip",
	".tar.bz2",
	".egg",
}

// IsDist reports whether a file name is a distribution.
func IsDist(file string) bool {
	return slices.ContainsFunc(distExts, func(ext string) bool {
		return strings.HasSuffix(file, ext)
	})
}

// ParseFilename extracts the project name and version from a wheel
// ({name}-{version}(-{build})?-{py}-{abi}-{platform}.whl) or an sdist
// ({name}-{version}.tar.gz) file name.
func ParseFilename(file string) (name, version string, ok bool) {
	if base, found := strings.CutSuffix(file, ".whl"); found {
		parts := strings.Split(base, "-")
		if len(parts) < 5 {
			return "", "", false
		}

		return parts[0], parts[1], true
	}

	for _, ext := range distExts {
		if base, found := strings.CutSuffix(file, ext); found {
			i := strings.LastIndex(base, "-")
			if i <= 0 {
				return "", "", false
			}

			return base[:i], base[i+1:], true
		}
	}

	return "", "", false
}

// Service implements the index on top of repo.Service.
type Service struct {
	repos *repository.Service
	log   *slog.Logger
	now   func() time.Time
}

// NewService returns a Service.
func NewService(repos *repository.Service, log *slog.Logger) *Service {
	repos.RegisterMetadata(func(name string) bool {
		return strings.HasSuffix(name, ".pypi.html")
	})

	s := &Service{
		repos: repos,
		log:   log,
		now:   time.Now,
	}
	repos.SetUpstreamCheck(Format, s.checkDistribution)
	repos.RegisterHook(Format, s.indexDistribution)

	return s
}

func (s *Service) indexDistribution(ctx context.Context, r repository.Repository, item repository.Item) error {
	if r.Class != "local" || !IsDist(item.Name) {
		return nil
	}

	props, err := s.repos.Properties(ctx, item)
	if err != nil || len(props["pypi.normalized.name"]) > 0 {
		return err
	}

	name, version, ok := ParseFilename(item.Name)
	if !ok || !ValidName(name) || !versionRe.MatchString(version) {
		return nil
	}

	return s.repos.SetProperties(ctx, item, repository.Properties{
		"pypi.name": {
			name,
		},
		"pypi.version": {
			version,
		},
		"pypi.normalized.name": {
			Normalize(name),
		},
	}, false)
}

// Upload describes a distribution uploaded with the legacy API.
type Upload struct {
	Repository, User    string
	Name, Version, File string
	Summary, RequiresPy string
	SHA256, MD5         string
	Body                io.Reader
}

// Store deploys an uploaded distribution at its layout path.
func (s *Service) Store(ctx context.Context, u Upload) (repository.Item, error) {
	if u.File == "" || strings.ContainsAny(u.File, `/\`) || !IsDist(u.File) {
		return repository.Item{}, fmt.Errorf("%w: file name %q", ErrInvalid, u.File)
	}

	if u.Name == "" || u.Version == "" {
		name, version, ok := ParseFilename(u.File)
		if !ok {
			return repository.Item{}, fmt.Errorf("%w: cannot tell name and version of %q", ErrInvalid, u.File)
		}

		u.Name, u.Version = cmp.Or(u.Name, name), cmp.Or(u.Version, version)
	}

	if !ValidName(u.Name) || !versionRe.MatchString(u.Version) {
		return repository.Item{}, fmt.Errorf("%w: name %q or version %q", ErrInvalid, u.Name, u.Version)
	}

	props := repository.Properties{
		"pypi.name": {
			u.Name,
		},
		"pypi.version": {
			u.Version,
		},
		"pypi.normalized.name": {
			Normalize(u.Name),
		},
	}
	if u.Summary != "" {
		props["pypi.summary"] = []string{
			u.Summary,
		}
	}

	if u.RequiresPy != "" {
		props["pypi.requires.python"] = []string{
			u.RequiresPy,
		}
	}

	return s.repos.Deploy(ctx, repository.DeployInput{
		Repository: u.Repository,
		Path:       Normalize(u.Name) + "/" + u.Version + "/" + u.File,
		Body:       u.Body,
		User:       u.User,
		Props:      props,
		Expected: repository.Checksums{
			SHA256: strings.ToLower(u.SHA256),
			MD5:    strings.ToLower(u.MD5),
		},
	})
}

// Link is one distribution on a project's simple page.
type Link struct {
	File           string
	Href           string
	SHA256         string
	RequiresPython string
}

// Links lists a project's distributions across r's members; the first
// member with a file of a given name wins.
func (s *Service) Links(ctx context.Context, r repository.Repository, name string) ([]Link, error) {
	norm := Normalize(name)

	var links []Link

	seen := map[string]bool{}
	found := false

	var upstreamErr error

	for _, m := range s.repos.Flatten(ctx, r, norm) {
		var (
			ls  []Link
			err error
		)
		if m.Class == "remote" {
			ls, err = s.remoteLinks(ctx, m, norm)
		} else {
			ls, err = s.localLinks(ctx, m.Name, norm)
		}

		switch {
		case err == nil:
			found = true
		case errors.Is(err, repository.ErrUpstream):
			upstreamErr = err
			continue
		case errors.Is(err, ErrNotFound):
			continue
		default:
			return nil, err
		}

		for _, l := range ls {
			if !seen[l.File] {
				seen[l.File] = true
				links = append(links, l)
			}
		}
	}

	switch {
	case !found && upstreamErr != nil:
		return nil, upstreamErr
	case !found:
		return nil, ErrNotFound
	}

	return links, nil
}

func (s *Service) localLinks(ctx context.Context, repoKey, norm string) ([]Link, error) {
	items, err := s.repos.FilesWithProperty(ctx, repoKey, "pypi.normalized.name", norm)
	if err != nil {
		return nil, err
	}

	var links []Link

	for _, it := range items {
		if !IsDist(it.Name) {
			continue
		}

		l := Link{
			File:   it.Name,
			Href:   "packages/" + escapePath(it.Path()),
			SHA256: it.Checksums.SHA256,
		}
		if props, err := s.repos.Properties(ctx, it); err == nil && len(props["pypi.requires.python"]) > 0 {
			l.RequiresPython = props["pypi.requires.python"][0]
		}

		links = append(links, l)
	}

	if len(links) == 0 {
		return nil, ErrNotFound
	}

	slices.SortFunc(links, func(a, b Link) int {
		return strings.Compare(a.File, b.File)
	})

	return links, nil
}

// Projects lists the normalized names of the projects in r's local
// members.
func (s *Service) Projects(ctx context.Context, r repository.Repository) ([]string, error) {
	var names []string

	for _, m := range s.repos.Flatten(ctx, r, "") {
		if m.Class == "remote" {
			continue
		}

		distributions, err := s.repos.FilesWithPropertyKey(ctx, m.Name, "pypi.normalized.name")
		if err != nil {
			return nil, err
		}

		for _, distribution := range distributions {
			names = append(names, distribution.Value)
		}
	}

	slices.Sort(names)

	return slices.Compact(names), nil
}

// Package resolves a file path below packages/ through r's members.
func (s *Service) Package(ctx context.Context, r repository.Repository, p string) (repository.Item, error) {
	var upstreamErr error

	for _, m := range s.repos.Flatten(ctx, r, projectOf(path.Base(p))) {
		item, err := s.repos.Resolve(ctx, m.Name, p)
		switch {
		case err == nil && !item.IsFolder():
			return item, nil
		case err == nil, errors.Is(err, repository.ErrNotFound):
		case errors.Is(err, repository.ErrUpstream):
			upstreamErr = err
		default:
			return repository.Item{}, err
		}
	}

	if upstreamErr != nil {
		return repository.Item{}, upstreamErr
	}

	return repository.Item{}, ErrNotFound
}

func (s *Service) remoteLinks(ctx context.Context, r repository.Repository, norm string) ([]Link, error) {
	page, err := s.repos.ResolveURL(ctx, r, registryURL(r, s.repos)+"/simple/"+norm+"/", ".pypi/"+norm+".pypi.html")
	if errors.Is(err, repository.ErrNotFound) {
		return nil, ErrNotFound
	}

	if err != nil {
		return nil, err
	}

	rc, err := s.repos.Open(ctx, page, nil)
	if err != nil {
		return nil, err
	}

	defer func() {
		_ = rc.Close()
	}()

	body, err := io.ReadAll(io.LimitReader(rc, 32<<20))
	if err != nil {
		return nil, err
	}

	base, err := url.Parse(registryURL(r, s.repos) + "/simple/" + norm + "/")
	if err != nil {
		return nil, fmt.Errorf("%w: index URL of %s: %w", repository.ErrInvalidRepository, r.Name, err)
	}

	files, _ := url.Parse(s.repos.UpstreamOf(r).URL)

	links := parseSimple(string(body), base, files)

	if cooldown := r.Upstream().Cooldown(); cooldown > 0 {
		times, err := s.uploadTimes(ctx, r, norm)
		if err != nil {
			return nil, err
		}

		cutoff := s.now().Add(-cooldown)
		links = slices.DeleteFunc(links, func(l Link) bool {
			uploaded, ok := times[l.File]
			return ok && uploaded.After(cutoff)
		})
	}

	offered := make([]repository.Version, 0, len(links))
	for _, l := range links {
		v := repository.Version{
			Version: versionOf(norm, l.File),
		}
		if strings.HasPrefix(l.Href, "packages/") {
			v.Path, _ = url.PathUnescape(l.Href)
		}

		offered = append(offered, v)
	}

	refused, err := s.repos.RefusedVersions(ctx, r, "pypi", norm, offered)
	if err != nil {
		return nil, err
	}

	if len(refused) > 0 {
		links = slices.DeleteFunc(links, func(l Link) bool {
			return refused[versionOf(norm, l.File)]
		})
	}

	if !repository.Restricted(ctx, r.Name) {
		return links, nil
	}

	visible := links[:0]
	for _, l := range links {
		p, err := url.PathUnescape(l.Href)
		if strings.Contains(l.Href, "://") || err == nil && repository.Visible(ctx, r.Name, p) {
			visible = append(visible, l)
		}
	}

	if len(visible) == 0 {
		return nil, ErrNotFound
	}

	return visible, nil
}

func registryURL(r repository.Repository, repos *repository.Service) string {
	if u := r.Upstream().IndexURL; u != "" {
		return strings.TrimSuffix(u, "/")
	}

	return "https://pypi.org"
}

func parseSimple(page string, base, files *url.URL) []Link {
	var links []Link

	for _, m := range anchorRe.FindAllStringSubmatch(page, -1) {
		attrs := map[string]string{}
		for _, a := range attrRe.FindAllStringSubmatch(m[1], -1) {
			attrs[strings.ToLower(a[1])] = html.UnescapeString(a[2])
		}

		href, err := url.Parse(attrs["href"])
		if err != nil || attrs["href"] == "" {
			continue
		}

		abs := base.ResolveReference(href)

		file := path.Base(abs.Path)
		if !IsDist(file) {
			continue
		}

		l := Link{
			File:           file,
			RequiresPython: attrs["data-requires-python"],
			Href:           abs.String(),
		}
		if sha, ok := strings.CutPrefix(abs.Fragment, "sha256="); ok {
			l.SHA256 = sha
		}

		if files != nil && abs.Host == files.Host {
			l.Href = "packages/" + strings.TrimPrefix(abs.EscapedPath(), "/")
		}

		links = append(links, l)
	}

	return links
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}

	return strings.Join(parts, "/")
}

// Offers reports whether r's index for project name offers a distribution
// of version: a remote or virtual repository leaves out versions cooling
// down, quarantined, known malicious or blocked.
func (s *Service) Offers(ctx context.Context, r repository.Repository, name, version string) (bool, error) {
	links, err := s.Links(ctx, r, name)
	if errors.Is(err, ErrNotFound) || errors.Is(err, repository.ErrNotFound) {
		return false, nil
	}

	if err != nil {
		return false, err
	}

	for _, l := range links {
		if versionOf(name, l.File) == version {
			return true, nil
		}
	}

	return false, nil
}
