// Package gems implements RubyGems repositories on Relic: gem push, the
// compact index
// (versions, info/<name>) bundler and RubyGems 3 use, the full index
// (specs.4.8.gz and quick gemspecs, in Ruby's Marshal format) and the
// dependency API.
package gems

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/md5" //nolint:gosec // the compact index uses MD5 as a cache key
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"relic/internal/artifacts/repository"

	"go.yaml.in/yaml/v3"
)

// Format is the repository format this package serves.
const Format = repository.FormatRubyGems

// Errors mapped by the API layer.
var (
	ErrNotFound = errors.New("gem not found")
	ErrInvalid  = errors.New("invalid gem")
	ErrExists   = errors.New("gem version already exists")
)

var (
	nameRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
	versionRe = regexp.MustCompile(`^[0-9]+[0-9A-Za-z.-]*$`)
)

// Requirement is one version constraint, e.g. {">=", "0"}.
type Requirement struct {
	Op, Version string
}

// Dependency is a gem dependency.
type Dependency struct {
	Name         string
	Type         string
	Requirements []Requirement
}

// Spec is the metadata of one gem file.
type Spec struct {
	Name, Version, Platform    string
	Summary, Description       string
	Authors                    []string
	Email, Homepage            string
	Licenses                   []string
	RequiredRuby, RequiredGems []Requirement
	Dependencies               []Dependency
	Metadata                   map[string]string
	RubygemsVersion            string
	SHA256                     string
}

// FullName is <name>-<version>[-<platform>], the gem file's base name.
func (s Spec) FullName() string {
	if s.Platform == "" || s.Platform == "ruby" {
		return s.Name + "-" + s.Version
	}

	return s.Name + "-" + s.Version + "-" + s.Platform
}

// Prerelease reports whether the version has a letter (RubyGems' rule).
func (s Spec) Prerelease() bool {
	return strings.ContainsAny(strings.ToLower(s.Version), "abcdefghijklmnopqrstuvwxyz")
}

// Service implements gem repositories on top of repo.Service.
type Service struct {
	repos *repository.Service
	log   *slog.Logger

	mu    sync.Mutex
	specs map[string]Spec
}

// NewService returns a Service and registers remote index files as
// metadata, re-fetched after the cache period.
func NewService(repos *repository.Service, log *slog.Logger) *Service {
	repos.RegisterMetadata(func(name string) bool {
		return name == "versions" || strings.HasSuffix(name, "specs.4.8.gz") || strings.HasPrefix(name, "info-")
	})

	return &Service{
		repos: repos,
		log:   log,
		specs: map[string]Spec{},
	}
}

// ReadSpec reads a .gem's metadata (a tar holding metadata.gz: YAML).
func ReadSpec(r io.Reader) (Spec, error) {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return Spec{}, fmt.Errorf("%w: no metadata.gz", ErrInvalid)
		}

		if err != nil {
			return Spec{}, fmt.Errorf("%w: %w", ErrInvalid, err)
		}

		if hdr.Name != "metadata.gz" {
			continue
		}

		gz, err := gzip.NewReader(io.LimitReader(tr, 16<<20))
		if err != nil {
			return Spec{}, fmt.Errorf("%w: %w", ErrInvalid, err)
		}

		raw, err := io.ReadAll(io.LimitReader(gz, 16<<20))
		if err != nil {
			return Spec{}, fmt.Errorf("%w: %w", ErrInvalid, err)
		}

		return parseSpec(raw)
	}
}

func parseSpec(raw []byte) (Spec, error) {
	var m map[string]any
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return Spec{}, fmt.Errorf("%w: metadata: %w", ErrInvalid, err)
	}

	s := Spec{
		Name:            str(m["name"]),
		Version:         str(dig(m, "version", "version")),
		Platform:        str(m["platform"]),
		Summary:         str(m["summary"]),
		Description:     str(m["description"]),
		Email:           firstStr(m["email"]),
		Homepage:        str(m["homepage"]),
		Authors:         strs(m["authors"]),
		Licenses:        strs(m["licenses"]),
		RequiredRuby:    requirements(m["required_ruby_version"]),
		RequiredGems:    requirements(m["required_rubygems_version"]),
		RubygemsVersion: str(m["rubygems_version"]),
		Metadata:        map[string]string{},
	}
	if s.Platform == "" {
		s.Platform = "ruby"
	}

	if md, ok := m["metadata"].(map[string]any); ok {
		for k, v := range md {
			s.Metadata[k] = str(v)
		}
	}

	deps, _ := m["dependencies"].([]any)
	for _, d := range deps {
		dm, ok := d.(map[string]any)
		if !ok {
			continue
		}

		s.Dependencies = append(s.Dependencies, Dependency{
			Name:         str(dm["name"]),
			Type:         strings.TrimPrefix(str(dm["type"]), ":"),
			Requirements: requirements(dm["requirement"]),
		})
	}

	if !nameRe.MatchString(s.Name) || !versionRe.MatchString(s.Version) {
		return Spec{}, fmt.Errorf("%w: name %q or version %q", ErrInvalid, s.Name, s.Version)
	}

	return s, nil
}

func requirements(v any) []Requirement {
	reqs, _ := dig(v, "requirements").([]any)

	var out []Requirement

	for _, r := range reqs {
		pair, ok := r.([]any)
		if !ok || len(pair) != 2 {
			continue
		}

		out = append(out, Requirement{
			Op:      str(pair[0]),
			Version: str(dig(pair[1], "version")),
		})
	}

	if len(out) == 0 {
		out = []Requirement{
			{
				">=",
				"0",
			},
		}
	}

	return out
}

func dig(v any, keys ...string) any {
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}

		v = m[k]
	}

	return v
}

func str(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	default:
		return fmt.Sprint(x)
	}
}

func strs(v any) []string {
	list, _ := v.([]any)

	out := make([]string, 0, len(list))
	for _, x := range list {
		out = append(out, str(x))
	}

	return out
}

func firstStr(v any) string {
	if list, ok := v.([]any); ok {
		if len(list) == 0 {
			return ""
		}

		return str(list[0])
	}

	return str(v)
}

// Push stores a pushed gem.
func (s *Service) Push(ctx context.Context, repoKey string, body []byte, user string) (Spec, error) {
	spec, err := ReadSpec(bytes.NewReader(body))
	if err != nil {
		return Spec{}, err
	}

	p := "gems/" + spec.FullName() + ".gem"

	_, err = s.repos.Stat(ctx, repoKey, p)
	if err == nil {
		return Spec{}, fmt.Errorf("%w: %s", ErrExists, spec.FullName())
	}

	if !errors.Is(err, repository.ErrNotFound) {
		return Spec{}, err
	}

	_, err = s.repos.Deploy(ctx, repository.DeployInput{
		Repository: repoKey,
		Path:       p,
		Body:       bytes.NewReader(body),
		User:       user,
		Props: repository.Properties{
			"gem.name": {
				spec.Name,
			},
			"gem.version": {
				spec.Version,
			},
			"gem.platform": {
				spec.Platform,
			},
		},
	})

	return spec, err
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

// Locals returns the specs of every gem in r's local members.
func (s *Service) Locals(ctx context.Context, r repository.Repository) ([]Spec, error) {
	var out []Spec

	seen := map[string]bool{}

	for _, m := range s.members(ctx, r, 0) {
		if m.Class == "remote" {
			continue
		}

		dir, err := s.repos.Stat(ctx, m.Name, "gems")
		if errors.Is(err, repository.ErrNotFound) {
			continue
		}

		if err != nil {
			return nil, err
		}

		kids, err := s.repos.Children(ctx, dir)
		if err != nil {
			return nil, err
		}

		for _, k := range kids {
			if k.IsFolder() || !strings.HasSuffix(k.Name, ".gem") || seen[k.Name] {
				continue
			}

			spec, err := s.spec(ctx, k)
			if err != nil {
				s.log.WarnContext(ctx, "skipping unreadable gem", "repo", k.Repository, "path", k.Path(), "error", err)
				continue
			}

			seen[k.Name] = true

			out = append(out, spec)
		}
	}

	slices.SortFunc(out, func(a, b Spec) int {
		return strings.Compare(a.FullName(), b.FullName())
	})

	return out, nil
}

func (s *Service) spec(ctx context.Context, item repository.Item) (Spec, error) {
	s.mu.Lock()
	spec, ok := s.specs[item.Checksums.SHA256]
	s.mu.Unlock()

	if ok {
		return spec, nil
	}

	rc, err := s.repos.Open(ctx, item, nil)
	if err != nil {
		return Spec{}, err
	}

	defer func() {
		_ = rc.Close()
	}()

	spec, err = ReadSpec(rc)
	if err != nil {
		return Spec{}, err
	}

	spec.SHA256 = item.Checksums.SHA256

	s.mu.Lock()
	if len(s.specs) > 50000 {
		clear(s.specs)
	}

	s.specs[item.Checksums.SHA256] = spec
	s.mu.Unlock()

	return spec, nil
}

// Gem finds a .gem file by file name through r's members, fetching it from
// a remote's upstream on a miss.
func (s *Service) Gem(ctx context.Context, r repository.Repository, file string) (repository.Item, error) {
	var upstreamErr error

	for _, m := range s.members(ctx, r, 0) {
		it, err := s.repos.Resolve(ctx, m.Name, "gems/"+file)
		switch {
		case err == nil && !it.IsFolder():
			return it, nil
		case errors.Is(err, repository.ErrUpstream):
			upstreamErr = err
		case err != nil && !errors.Is(err, repository.ErrNotFound):
			return repository.Item{}, err
		}
	}

	if upstreamErr != nil {
		return repository.Item{}, upstreamErr
	}

	return repository.Item{}, ErrNotFound
}

// InfoLine is one version line of a compact index info file:
// "<version>[-<platform>] <deps>|checksum:<sha256>[,ruby:<reqs>]".
func InfoLine(s Spec) string {
	v := s.Version
	if s.Platform != "" && s.Platform != "ruby" {
		v += "-" + s.Platform
	}

	var deps []string

	for _, d := range s.Dependencies {
		if d.Type == "development" {
			continue
		}

		deps = append(deps, d.Name+":"+reqString(d.Requirements))
	}

	line := v + " " + strings.Join(deps, ",") + "|checksum:" + s.SHA256
	if r := reqString(s.RequiredRuby); r != ">= 0" {
		line += ",ruby:" + r
	}

	if r := reqString(s.RequiredGems); r != ">= 0" {
		line += ",rubygems:" + r
	}

	return line
}

func reqString(reqs []Requirement) string {
	parts := make([]string, 0, len(reqs))
	for _, r := range reqs {
		parts = append(parts, r.Op+" "+r.Version)
	}

	return strings.Join(parts, "&")
}

// Info returns the compact index info file of a gem across r's members:
// local gems' lines, then remote members' upstream lines.
func (s *Service) Info(ctx context.Context, r repository.Repository, name string, locals []Spec) ([]byte, error) {
	var b strings.Builder
	b.WriteString("---\n")

	seen := map[string]bool{}
	found := false

	for _, spec := range locals {
		if spec.Name != name {
			continue
		}

		found = true
		line := InfoLine(spec)
		seen[strings.Fields(line)[0]] = true
		b.WriteString(line + "\n")
	}

	var upstreamErr error

	for _, m := range s.members(ctx, r, 0) {
		if m.Class != "remote" {
			continue
		}

		body, err := s.remoteFile(ctx, m, "info/"+name, "info-"+name)
		if errors.Is(err, repository.ErrUpstream) {
			upstreamErr = err
			continue
		}

		if err != nil {
			continue
		}

		found = true
		restricted := repository.Restricted(ctx, m.Name)

		for line := range strings.SplitSeq(string(body), "\n") {
			key, ok := infoLineVersion(line)
			if !ok {
				continue
			}

			if restricted && !repository.Visible(ctx, m.Name, "gems/"+name+"-"+key+".gem") {
				continue
			}

			if !seen[key] {
				seen[key] = true

				b.WriteString(line + "\n")
			}
		}
	}

	if !found {
		if upstreamErr != nil {
			return nil, upstreamErr
		}

		return nil, ErrNotFound
	}

	return []byte(b.String()), nil
}

func infoLineVersion(line string) (string, bool) {
	fields := strings.Fields(line)
	if len(fields) == 0 || line == "---" {
		return "", false
	}

	return fields[0], true
}

// Versions returns the compact index versions file: the upstream file of
// the first remote member, if any, with lines for local gems appended.
func (s *Service) Versions(
	ctx context.Context,
	r repository.Repository,
	locals []Spec,
	infos map[string][]byte,
) ([]byte, error) {
	for _, m := range s.members(ctx, r, 0) {
		if m.Class != "remote" {
			continue
		}

		upstream, err := s.remoteFile(ctx, m, "versions", "versions")
		if err != nil {
			return nil, err
		}

		if repository.Restricted(ctx, m.Name) {
			upstream = visibleVersions(ctx, m.Name, upstream)
		}

		if len(upstream) > 0 && upstream[len(upstream)-1] != '\n' {
			upstream = append(upstream, '\n')
		}

		return append(upstream, versionLines(locals, infos)...), nil
	}

	head := "created_at: " + time.Now().UTC().Format(time.RFC3339) + "\n---\n"

	return append([]byte(head), versionLines(locals, infos)...), nil
}

func visibleVersions(ctx context.Context, repoKey string, body []byte) []byte {
	var b strings.Builder

	header := true
	for line := range strings.SplitSeq(string(body), "\n") {
		if header || line == "" {
			b.WriteString(line + "\n")
			header = header && line != "---"

			continue
		}

		f := strings.Fields(line)
		if len(f) != 3 {
			continue
		}

		var kept []string

		for v := range strings.SplitSeq(f[1], ",") {
			if repository.Visible(ctx, repoKey, "gems/"+f[0]+"-"+strings.TrimPrefix(v, "-")+".gem") {
				kept = append(kept, v)
			}
		}

		if len(kept) > 0 {
			b.WriteString(f[0] + " " + strings.Join(kept, ",") + " " + f[2] + "\n")
		}
	}

	return []byte(strings.TrimSuffix(b.String(), "\n") + "\n")
}

func versionLines(locals []Spec, infos map[string][]byte) []byte {
	var b strings.Builder

	byName := map[string][]string{}

	var names []string

	for _, s := range locals {
		if _, ok := byName[s.Name]; !ok {
			names = append(names, s.Name)
		}

		v := s.Version
		if s.Platform != "ruby" && s.Platform != "" {
			v += "-" + s.Platform
		}

		byName[s.Name] = append(byName[s.Name], v)
	}

	slices.Sort(names)

	for _, n := range names {
		sum := md5.Sum(infos[n]) //nolint:gosec // compact index cache key
		b.WriteString(n + " " + strings.Join(byName[n], ",") + " " + hex.EncodeToString(sum[:]) + "\n")
	}

	return []byte(b.String())
}

func (s *Service) remoteFile(ctx context.Context, r repository.Repository, p, cachePath string) ([]byte, error) {
	base := strings.TrimSuffix(s.repos.UpstreamOf(r).URL, "/")

	item, err := s.repos.ResolveURL(ctx, r, base+"/"+p, ".gems/"+cachePath)
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

	return io.ReadAll(io.LimitReader(rc, 64<<20))
}

// RemoteQuickSpec fetches a quick gemspec for a gem not stored locally.
func (s *Service) RemoteQuickSpec(ctx context.Context, r repository.Repository, file string) ([]byte, error) {
	var upstreamErr error

	for _, m := range s.members(ctx, r, 0) {
		if m.Class != "remote" {
			continue
		}

		body, err := s.remoteFile(ctx, m, "quick/Marshal.4.8/"+file, "quick-"+file)
		if err == nil {
			return body, nil
		}

		if errors.Is(err, repository.ErrUpstream) {
			upstreamErr = err
		}
	}

	if upstreamErr != nil {
		return nil, upstreamErr
	}

	return nil, ErrNotFound
}

func gemVersion(v string) UserMarshal {
	return UserMarshal{
		Class: "Gem::Version",
		Data: []any{
			v,
		},
	}
}

func gemRequirement(reqs []Requirement) UserMarshal {
	list := make([]any, 0, len(reqs))
	for _, r := range reqs {
		list = append(list, []any{
			r.Op,
			gemVersion(r.Version),
		})
	}

	return UserMarshal{
		Class: "Gem::Requirement",
		Data: []any{
			list,
		},
	}
}

// SpecsIndex is specs.4.8 (Marshal of [[name, Gem::Version, platform]]),
// gzipped, for the given specs.
func SpecsIndex(specs []Spec) ([]byte, error) {
	list := make([]any, 0, len(specs))
	for _, s := range specs {
		list = append(list, []any{
			s.Name,
			gemVersion(s.Version),
			s.Platform,
		})
	}

	var buf bytes.Buffer

	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(Marshal(list)); err != nil {
		return nil, err
	}

	if err := zw.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// QuickSpec is quick/Marshal.4.8/<full name>.gemspec.rz: a zlib-deflated
// Marshal of the Gem::Specification, in the field order of
// Gem::Specification#_dump.
func QuickSpec(s Spec) ([]byte, error) {
	deps := make([]any, 0, len(s.Dependencies))
	for _, d := range s.Dependencies {
		req := gemRequirement(d.Requirements)

		typ := d.Type
		if typ == "" {
			typ = "runtime"
		}

		deps = append(deps, Object{
			Class: "Gem::Dependency",
			Ivars: []Ivar{
				{
					"@name",
					d.Name,
				},
				{
					"@requirement",
					req,
				},
				{
					"@type",
					Symbol(typ),
				},
				{
					"@prerelease",
					false,
				},
				{
					"@version_requirements",
					req,
				},
			},
		})
	}

	rgVersion := s.RubygemsVersion
	if rgVersion == "" {
		rgVersion = "3.4.0"
	}

	fields := []any{
		rgVersion,
		4,
		s.Name,
		gemVersion(s.Version),
		nil,
		s.Summary,
		gemRequirement(s.RequiredRuby),
		gemRequirement(s.RequiredGems),
		s.Platform,
		deps,
		"",
		s.Email,
		s.Authors,
		s.Description,
		s.Homepage,
		true,
		s.Platform,
		s.Licenses,
		s.Metadata,
	}

	var buf bytes.Buffer

	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write(Marshal(UserDefined{
		Class: "Gem::Specification",
		Data:  Marshal(fields),
	})); err != nil {
		return nil, err
	}

	if err := zw.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// Dependencies is the dependency API's answer (Marshal of an array of
// hashes) for the named gems among specs.
func Dependencies(specs []Spec, names []string) []byte {
	var out []any

	for _, s := range specs {
		if !slices.Contains(names, s.Name) {
			continue
		}

		var deps []any

		for _, d := range s.Dependencies {
			if d.Type != "development" {
				deps = append(deps, []any{
					d.Name,
					reqString(d.Requirements),
				})
			}
		}

		if deps == nil {
			deps = []any{}
		}

		out = append(out, Hash{
			{
				Symbol("name"),
				s.Name,
			},
			{
				Symbol("number"),
				s.Version,
			},
			{
				Symbol("platform"),
				s.Platform,
			},
			{
				Symbol("dependencies"),
				deps,
			},
		})
	}

	if out == nil {
		out = []any{}
	}

	return Marshal(out)
}
