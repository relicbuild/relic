// Package conan implements Conan 2 repositories (contract
// conan.md): the v2 revisions API over Relic's
// storage layout
//
//	<user>/<name>/<version>/<channel>/<rrev>/export/<file>
//	<user>/<name>/<version>/<channel>/<rrev>/package/<pkgid>/<prev>/<file>
//
// with "_" for a missing user or channel.
package conan

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"relic/internal/artifacts/repository"
)

// Format is the repository format this package serves.
const Format = repository.FormatConan

// Domain errors.
var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid reference")
)

// Manifest is the file whose presence publishes a revision.
const Manifest = "conanmanifest.txt"

// TimeFormat is how revision times are rendered (Python isoformat, UTC).
const TimeFormat = "2006-01-02T15:04:05.000000+00:00"

const jsonCacheSuffix = ".conan.json"

const maxFile = 16 << 20

var errTooLarge = errors.New("file too large")

var segment = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.+-]{0,100}$`)

// Ref is a recipe reference: name/version@user/channel ("_" when absent).
type Ref struct {
	Name, Version, User, Channel string
}

// ParseRef validates the four path segments of a reference.
func ParseRef(name, version, user, channel string) (Ref, error) {
	r := Ref{
		Name:    name,
		Version: version,
		User:    user,
		Channel: channel,
	}
	for _, s := range []string{
		name,
		version,
		user,
		channel,
	} {
		if !segment.MatchString(s) {
			return Ref{}, fmt.Errorf("%w: %q", ErrInvalid, s)
		}
	}

	return r, nil
}

// Dir is the reference's folder in a repository.
func (r Ref) Dir() string { return r.User + "/" + r.Name + "/" + r.Version + "/" + r.Channel }

// APIPath is the reference in v2 URLs.
func (r Ref) APIPath() string { return r.Name + "/" + r.Version + "/" + r.User + "/" + r.Channel }

// String is the search result form, name/version@user/channel.
func (r Ref) String() string { return r.Name + "/" + r.Version + "@" + r.User + "/" + r.Channel }

// Revision is a recipe or package revision.
type Revision struct {
	Revision string `json:"revision"`
	Time     string `json:"time"`
}

// Service serves Conan repositories.
type Service struct {
	repos *repository.Service
	log   *slog.Logger
}

// NewService returns a Service and registers cached upstream API answers as
// metadata.
func NewService(repos *repository.Service, log *slog.Logger) *Service {
	repos.RegisterMetadata(func(name string) bool {
		return strings.HasSuffix(name, jsonCacheSuffix)
	})

	return &Service{
		repos: repos,
		log:   log,
	}
}

// Members lists the non-virtual repositories r resolves through.
func (s *Service) Members(ctx context.Context, r repository.Repository) []repository.Repository {
	if r.Class != "virtual" {
		return []repository.Repository{
			r,
		}
	}

	var out []repository.Repository

	for _, key := range s.repos.Members(r) {
		if m, err := s.repos.GetRepository(ctx, key); err == nil && m.Class != "virtual" {
			out = append(out, m)
		}
	}

	return out
}

func (s *Service) revisions(ctx context.Context, repoKey, dir, sub string) ([]Revision, error) {
	folder, err := s.repos.Stat(ctx, repoKey, dir)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	kids, err := s.repos.Children(ctx, folder)
	if err != nil {
		return nil, err
	}

	type timed struct {
		rev string
		t   time.Time
	}

	var found []timed

	for _, k := range kids {
		if !k.IsFolder() || k.Name == "package" {
			continue
		}

		mp := path.Join(dir, k.Name, sub, Manifest)

		m, err := s.repos.Stat(ctx, repoKey, mp)
		if errors.Is(err, repository.ErrNotFound) {
			continue
		}

		if err != nil {
			return nil, err
		}

		found = append(found, timed{
			k.Name,
			m.Created,
		})
	}

	slices.SortFunc(found, func(a, b timed) int {
		return b.t.Compare(a.t)
	})

	out := make([]Revision, 0, len(found))
	for _, f := range found {
		out = append(out, Revision{
			Revision: f.rev,
			Time:     f.t.UTC().Format(TimeFormat),
		})
	}

	return out, nil
}

// RecipeRevisions lists a local repository's recipe revisions.
func (s *Service) RecipeRevisions(ctx context.Context, repoKey string, ref Ref) ([]Revision, error) {
	return s.revisions(ctx, repoKey, ref.Dir(), "export")
}

// PackageRevisions lists a local repository's revisions of one binary.
func (s *Service) PackageRevisions(
	ctx context.Context,
	repoKey string,
	ref Ref,
	rrev, pkgID string,
) ([]Revision, error) {
	if !segment.MatchString(rrev) || !segment.MatchString(pkgID) {
		return nil, ErrInvalid
	}

	return s.revisions(ctx, repoKey, path.Join(ref.Dir(), rrev, "package", pkgID), "")
}

// Packages returns {pkgid: {"content": conaninfo.txt}} for a recipe
// revision's published binaries (the latest revision of each).
func (s *Service) Packages(
	ctx context.Context,
	repoKey string,
	ref Ref,
	rrev string,
) (map[string]map[string]string, error) {
	out := map[string]map[string]string{}

	if !segment.MatchString(rrev) {
		return nil, ErrInvalid
	}

	dir := path.Join(ref.Dir(), rrev, "package")

	folder, err := s.repos.Stat(ctx, repoKey, dir)
	if errors.Is(err, repository.ErrNotFound) {
		return out, nil
	}

	if err != nil {
		return nil, err
	}

	kids, err := s.repos.Children(ctx, folder)
	if err != nil {
		return nil, err
	}

	for _, k := range kids {
		if !k.IsFolder() {
			continue
		}

		revs, err := s.revisions(ctx, repoKey, path.Join(dir, k.Name), "")
		if err != nil {
			return nil, err
		}

		if len(revs) == 0 {
			continue
		}

		content, err := s.readFile(ctx, repoKey, path.Join(dir, k.Name, revs[0].Revision, "conaninfo.txt"))
		if err != nil && !errors.Is(err, repository.ErrNotFound) {
			return nil, err
		}

		out[k.Name] = map[string]string{
			"content": string(content),
		}
	}

	return out, nil
}

func (s *Service) readFile(ctx context.Context, repoKey, p string) ([]byte, error) {
	it, err := s.repos.Stat(ctx, repoKey, p)
	if err != nil {
		return nil, err
	}

	rc, err := s.repos.Open(ctx, it, nil)
	if err != nil {
		return nil, err
	}

	defer func() {
		_ = rc.Close()
	}()

	return readCapped(rc)
}

func readCapped(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxFile+1))
	if err != nil {
		return nil, err
	}

	if len(b) > maxFile {
		return nil, fmt.Errorf("%w: more than %d bytes", errTooLarge, maxFile)
	}

	return b, nil
}

// Files lists the files of a revision folder (recipe export or package
// revision), without metadata subfolders.
func (s *Service) Files(ctx context.Context, repoKey, dir string) (map[string]struct{}, error) {
	folder, err := s.repos.Stat(ctx, repoKey, dir)
	if err != nil {
		return nil, err
	}

	if _, err := s.repos.Stat(ctx, repoKey, path.Join(dir, Manifest)); err != nil {
		return nil, err
	}

	kids, err := s.repos.Children(ctx, folder)
	if err != nil {
		return nil, err
	}

	out := map[string]struct{}{}

	for _, k := range kids {
		if !k.IsFolder() {
			out[k.Name] = struct{}{}
		}
	}

	return out, nil
}

// Search lists a local repository's references matching an fnmatch-style
// pattern over name/version@user/channel.
func (s *Service) Search(ctx context.Context, repoKey, pattern string, ignoreCase bool) ([]string, error) {
	match, err := fnmatch(pattern, ignoreCase)
	if err != nil {
		return nil, fmt.Errorf("%w: pattern %q", ErrInvalid, pattern)
	}

	var (
		out  []string
		walk func(dir string, depth int, parts []string) error
	)

	walk = func(dir string, depth int, parts []string) error {
		var (
			folder repository.Item
			err    error
		)
		if dir == "" {
			folder, err = s.repos.Stat(ctx, repoKey, "")
		} else {
			folder, err = s.repos.Stat(ctx, repoKey, dir)
		}

		if errors.Is(err, repository.ErrNotFound) {
			return nil
		}

		if err != nil {
			return err
		}

		kids, err := s.repos.Children(ctx, folder)
		if err != nil {
			return err
		}

		for _, k := range kids {
			if !k.IsFolder() || strings.HasPrefix(k.Name, ".") {
				continue
			}

			next := append(slices.Clone(parts), k.Name)
			if depth < 3 {
				if err := walk(path.Join(dir, k.Name), depth+1, next); err != nil {
					return err
				}

				continue
			}

			ref := Ref{
				User:    next[0],
				Name:    next[1],
				Version: next[2],
				Channel: next[3],
			}
			if !match.MatchString(ref.String()) {
				continue
			}

			revs, err := s.RecipeRevisions(ctx, repoKey, ref)
			if err != nil {
				return err
			}

			if len(revs) > 0 {
				out = append(out, ref.String())
			}
		}

		return nil
	}
	if err := walk("", 0, nil); err != nil {
		return nil, err
	}

	slices.Sort(out)

	return out, nil
}

func fnmatch(pattern string, ignoreCase bool) (*regexp.Regexp, error) {
	if pattern == "" {
		pattern = "*"
	}

	if !strings.Contains(pattern, "@") {
		pattern += "*"
	}

	expr := regexp.QuoteMeta(pattern)

	expr = strings.NewReplacer(`\*`, ".*", `\?`, ".").Replace(expr)
	if ignoreCase {
		expr = "(?i)" + expr
	}

	return regexp.Compile("^" + expr + "$")
}

// Upstream fetches a JSON answer from a remote's upstream Conan server,
// cached as metadata at .conan/<apiPath>.
func (s *Service) Upstream(ctx context.Context, r repository.Repository, apiPath string) ([]byte, error) {
	base := strings.TrimSuffix(s.repos.UpstreamOf(r).URL, "/")
	cache := ".conan/" + strings.ReplaceAll(strings.TrimPrefix(apiPath, "/"), "?", "_q_") + jsonCacheSuffix

	item, err := s.repos.ResolveURL(ctx, r, base+"/"+apiPath, cache)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, ErrNotFound
	}

	if err != nil {
		return nil, err
	}

	rc, err := s.repos.Open(ctx, item, nil)
	if err != nil {
		return nil, err
	}

	defer func() {
		_ = rc.Close()
	}()

	return readCapped(rc)
}

// UpstreamFile resolves a file through a remote, cached at storePath.
func (s *Service) UpstreamFile(
	ctx context.Context,
	r repository.Repository,
	apiPath, storePath string,
) (repository.Item, error) {
	base := strings.TrimSuffix(s.repos.UpstreamOf(r).URL, "/")
	return s.repos.ResolveURL(ctx, r, base+"/"+apiPath, storePath)
}

// Newest picks the most recent revision across lists.
func Newest(lists ...[]Revision) (Revision, bool) {
	var all []Revision
	for _, l := range lists {
		all = append(all, l...)
	}

	if len(all) == 0 {
		return Revision{}, false
	}

	return slices.MaxFunc(
		all,
		func(a, b Revision) int {
			return cmp.Compare(parseTime(a.Time), parseTime(b.Time))
		},
	), true
}

// MergeRevisions merges revision lists, newest first, without duplicates.
func MergeRevisions(lists ...[]Revision) []Revision {
	seen := map[string]bool{}

	var out []Revision

	for _, l := range lists {
		for _, r := range l {
			if !seen[r.Revision] {
				seen[r.Revision] = true
				out = append(out, r)
			}
		}
	}

	slices.SortStableFunc(out, func(a, b Revision) int {
		return cmp.Compare(parseTime(b.Time), parseTime(a.Time))
	})

	return out
}

func parseTime(s string) int64 {
	for _, layout := range []string{
		time.RFC3339Nano,
		TimeFormat,
		"2006-01-02T15:04:05.999999-07:00",
		"2006-01-02T15:04:05-0700",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UnixNano()
		}
	}

	return 0
}

// DecodeRevisions reads an upstream {"revisions":[...]} answer.
func DecodeRevisions(raw []byte) ([]Revision, error) {
	var v struct {
		Revisions []Revision `json:"revisions"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("upstream revisions: %w", err)
	}

	return v.Revisions, nil
}
