// Package cargo implements Cargo registries (contract
// §1): the sparse index,
// crate downloads and `cargo publish`.
package cargo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/mod/semver"

	"relic/internal/artifacts/repository"
)

// Format is the repository format this package serves.
const Format = repository.FormatCargo

// Domain errors.
var (
	ErrNotFound = errors.New("crate not found")
	ErrExists   = errors.New("crate version already exists")
	ErrInvalid  = errors.New("invalid crate")
)

const (
	propName      = "cargo.name"
	propIndexName = "cargo.index_name"
	propVersion   = "cargo.version"
	propIndex     = "cargo.index"
)

// MaxPublish bounds a publish body.
const MaxPublish = 256 << 20

var (
	namePattern    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)
	versionPattern = regexp.MustCompile(`^[0-9A-Za-z.+-]{1,128}$`)
)

const indexCacheSuffix = ".cargo-index"

// Service serves Cargo repositories.
type Service struct {
	repos *repository.Service
	log   *slog.Logger
}

// NewService returns a Service and registers cached remote index files as
// metadata.
func NewService(repos *repository.Service, log *slog.Logger) *Service {
	repos.RegisterMetadata(func(name string) bool {
		return strings.HasSuffix(name, indexCacheSuffix)
	})

	return &Service{
		repos: repos,
		log:   log,
	}
}

// IndexPrefix is the sparse index directory for a crate name
// ("1/", "2/", "3/a/", "ab/cd/"), from the lowercased name.
func IndexPrefix(name string) string {
	return directoryPrefix(strings.ToLower(name))
}

func directoryPrefix(name string) string {
	switch len(name) {
	case 0:
		return ""
	case 1:
		return "1/"
	case 2:
		return "2/"
	case 3:
		return "3/" + name[:1] + "/"
	}

	return name[:2] + "/" + name[2:4] + "/"
}

// CratePath is where a crate is stored.
func CratePath(name, version string) string {
	return "crates/" + name + "/" + name + "-" + version + ".crate"
}

type publishDep struct {
	Name               string   `json:"name"`
	VersionReq         string   `json:"version_req"`
	Features           []string `json:"features"`
	Optional           bool     `json:"optional"`
	DefaultFeatures    bool     `json:"default_features"`
	Target             *string  `json:"target"`
	Kind               string   `json:"kind"`
	Registry           *string  `json:"registry"`
	ExplicitNameInToml *string  `json:"explicit_name_in_toml"`
}

type publishMeta struct {
	Name        string              `json:"name"`
	Vers        string              `json:"vers"`
	Deps        []publishDep        `json:"deps"`
	Features    map[string][]string `json:"features"`
	Links       *string             `json:"links"`
	RustVersion *string             `json:"rust_version"`
	Description *string             `json:"description"`
}

type indexDep struct {
	Name            string   `json:"name"`
	Req             string   `json:"req"`
	Features        []string `json:"features"`
	Optional        bool     `json:"optional"`
	DefaultFeatures bool     `json:"default_features"`
	Target          *string  `json:"target"`
	Kind            string   `json:"kind"`
	Registry        *string  `json:"registry"`
	Package         *string  `json:"package,omitempty"`
}

type indexLine struct {
	Name        string              `json:"name"`
	Vers        string              `json:"vers"`
	Deps        []indexDep          `json:"deps"`
	Cksum       string              `json:"cksum"`
	Features    map[string][]string `json:"features"`
	Features2   map[string][]string `json:"features2,omitempty"`
	Yanked      bool                `json:"yanked"`
	Links       *string             `json:"links"`
	V           int                 `json:"v,omitempty"`
	RustVersion *string             `json:"rust_version,omitempty"`
}

func parsePublish(body []byte) (publishMeta, []byte, error) {
	next := func() ([]byte, error) {
		if len(body) < 4 {
			return nil, fmt.Errorf("%w: truncated publish body", ErrInvalid)
		}

		n := binary.LittleEndian.Uint32(body[:4])

		body = body[4:]
		if uint64(n) > uint64(len(body)) {
			return nil, fmt.Errorf("%w: truncated publish body", ErrInvalid)
		}

		part := body[:n]
		body = body[n:]

		return part, nil
	}

	rawMeta, err := next()
	if err != nil {
		return publishMeta{}, nil, err
	}

	crate, err := next()
	if err != nil {
		return publishMeta{}, nil, err
	}

	var meta publishMeta
	if err := json.Unmarshal(rawMeta, &meta); err != nil {
		return publishMeta{}, nil, fmt.Errorf("%w: metadata: %w", ErrInvalid, err)
	}

	if !namePattern.MatchString(meta.Name) || !versionPattern.MatchString(meta.Vers) {
		return publishMeta{}, nil, fmt.Errorf("%w: invalid name or version", ErrInvalid)
	}

	return meta, crate, nil
}

func (m publishMeta) line(cksum string) indexLine {
	l := indexLine{
		Name:        m.Name,
		Vers:        m.Vers,
		Cksum:       cksum,
		Links:       m.Links,
		RustVersion: m.RustVersion,
		Deps:        []indexDep{},
		Features:    map[string][]string{},
	}
	for _, d := range m.Deps {
		dep := indexDep{
			Name:            d.Name,
			Req:             d.VersionReq,
			Features:        d.Features,
			Optional:        d.Optional,
			DefaultFeatures: d.DefaultFeatures,
			Target:          d.Target,
			Kind:            d.Kind,
			Registry:        d.Registry,
		}
		if dep.Features == nil {
			dep.Features = []string{}
		}

		if d.ExplicitNameInToml != nil && *d.ExplicitNameInToml != "" {
			pkg := d.Name
			dep.Name, dep.Package = *d.ExplicitNameInToml, &pkg
		}

		l.Deps = append(l.Deps, dep)
	}

	for k, vals := range m.Features {
		if slices.ContainsFunc(
			vals,
			func(v string) bool {
				return strings.HasPrefix(v, "dep:") || strings.Contains(v, "?/")
			},
		) {
			if l.Features2 == nil {
				l.Features2 = map[string][]string{}
			}

			l.Features2[k] = vals
			l.V = 2

			continue
		}

		l.Features[k] = vals
	}

	return l
}

// Publish stores a published crate in the local repository repoKey.
func (s *Service) Publish(ctx context.Context, repoKey string, body []byte, user string) (string, error) {
	meta, crate, err := parsePublish(body)
	if err != nil {
		return "", err
	}

	existing, err := s.localVersions(ctx, repoKey, strings.ToLower(meta.Name))
	if err != nil {
		return "", err
	}

	for _, l := range existing {
		if l.line.Vers == meta.Vers {
			return "", fmt.Errorf("%w: %s@%s", ErrExists, meta.Name, meta.Vers)
		}
	}

	sum := sha256.Sum256(crate)

	line, err := json.Marshal(meta.line(hex.EncodeToString(sum[:])))
	if err != nil {
		return "", fmt.Errorf("encode index line: %w", err)
	}

	_, err = s.repos.Deploy(ctx, repository.DeployInput{
		Repository: repoKey,
		Path:       CratePath(meta.Name, meta.Vers),
		Body:       bytes.NewReader(crate),
		User:       user,
		Expected: repository.Checksums{
			SHA256: hex.EncodeToString(sum[:]),
		},
		Props: repository.Properties{
			propName: {
				meta.Name,
			},
			propIndexName: {
				strings.ToLower(meta.Name),
			},
			propVersion: {
				meta.Vers,
			},
			propIndex: {
				string(line),
			},
		},
	})
	if err != nil {
		return "", err
	}

	return meta.Name + "@" + meta.Vers, nil
}

type localVersion struct {
	item repository.Item
	line indexLine
}

func (s *Service) localVersions(ctx context.Context, repoKey, lname string) ([]localVersion, error) {
	items, err := s.repos.FilesWithProperty(ctx, repoKey, propIndexName, lname)
	if err != nil {
		return nil, err
	}

	var out []localVersion

	for _, it := range items {
		props, err := s.repos.Properties(ctx, it)
		if err != nil {
			return nil, err
		}

		if len(props[propIndex]) == 0 {
			continue
		}

		var l indexLine
		if err := json.Unmarshal([]byte(props[propIndex][0]), &l); err != nil {
			s.log.WarnContext(
				ctx,
				"skipping crate with a bad index line",
				"repo",
				it.Repository,
				"path",
				it.Path(),
				"error",
				err,
			)

			continue
		}

		out = append(out, localVersion{
			item: it,
			line: l,
		})
	}

	return out, nil
}

// Index returns a crate's index file (one JSON line per version).
func (s *Service) Index(ctx context.Context, r repository.Repository, name string) ([]byte, error) {
	lname := strings.ToLower(name)
	if !namePattern.MatchString(lname) {
		return nil, ErrNotFound
	}

	if r.Class == "virtual" {
		return s.virtualIndex(ctx, r, name)
	}

	if r.Class == "remote" {
		base := strings.TrimSuffix(s.repos.UpstreamOf(r).URL, "/")

		body, err := s.remoteFile(
			ctx,
			r,
			base+"/"+IndexPrefix(lname)+lname,
			".index/"+IndexPrefix(lname)+lname+indexCacheSuffix,
		)
		if err != nil || !repository.Restricted(ctx, r.Name) {
			return body, err
		}

		return visibleLines(ctx, r.Name, body)
	}

	versions, err := s.localVersions(ctx, r.Name, lname)
	if err != nil {
		return nil, err
	}

	if len(versions) == 0 {
		return nil, ErrNotFound
	}

	slices.SortFunc(versions, func(a, b localVersion) int {
		return a.item.Created.Compare(b.item.Created)
	})

	var buf bytes.Buffer

	for _, v := range versions {
		line, err := json.Marshal(v.line)
		if err != nil {
			return nil, fmt.Errorf("encode index line: %w", err)
		}

		buf.Write(line)
		buf.WriteByte('\n')
	}

	return buf.Bytes(), nil
}

func visibleLines(ctx context.Context, repoKey string, body []byte) ([]byte, error) {
	var out bytes.Buffer

	for line := range bytes.SplitSeq(body, []byte("\n")) {
		var v struct {
			Name string `json:"name"`
			Vers string `json:"vers"`
		}
		if json.Unmarshal(line, &v) != nil || !repository.Visible(ctx, repoKey, CratePath(v.Name, v.Vers)) {
			continue
		}

		out.Write(line)
		out.WriteByte('\n')
	}

	if out.Len() == 0 {
		return nil, ErrNotFound
	}

	return out.Bytes(), nil
}

// Crate finds a crate file, fetching it from a remote's upstream on a miss.
func (s *Service) Crate(ctx context.Context, r repository.Repository, name, version string) (repository.Item, error) {
	if !namePattern.MatchString(name) || !versionPattern.MatchString(version) {
		return repository.Item{}, ErrNotFound
	}

	if r.Class == "virtual" {
		return s.virtualCrate(ctx, r, name, version)
	}

	p := CratePath(name, version)
	if r.Class != "remote" {
		it, err := s.repos.Stat(ctx, r.Name, p)
		if err == nil {
			return it, nil
		}

		if !errors.Is(err, repository.ErrNotFound) {
			return repository.Item{}, err
		}

		versions, err := s.localVersions(ctx, r.Name, strings.ToLower(name))
		if err != nil {
			return repository.Item{}, err
		}

		for _, v := range versions {
			if v.line.Vers == version {
				return v.item, nil
			}
		}

		return repository.Item{}, ErrNotFound
	}

	dl, err := s.remoteDownloadURL(ctx, r, name, version)
	if err != nil {
		return repository.Item{}, err
	}

	return s.repos.ResolveURL(ctx, r, dl, p)
}

func (s *Service) remoteDownloadURL(
	ctx context.Context,
	r repository.Repository,
	name, version string,
) (string, error) {
	base := strings.TrimSuffix(s.repos.UpstreamOf(r).URL, "/")

	raw, err := s.remoteFile(ctx, r, base+"/config.json", ".index/config.json"+indexCacheSuffix)
	if err != nil {
		return "", err
	}

	var cfg struct {
		DL string `json:"dl"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil || cfg.DL == "" {
		return "", fmt.Errorf("%w: upstream config.json has no dl", repository.ErrUpstream)
	}

	return downloadURL(cfg.DL, name, version)
}

func downloadURL(dl, name, version string) (string, error) {
	markers := strings.NewReplacer(
		"{crate}",
		name,
		"{version}",
		version,
		"{prefix}",
		strings.TrimSuffix(directoryPrefix(name), "/"),
		"{lowerprefix}",
		strings.TrimSuffix(IndexPrefix(name), "/"),
	)
	if !strings.Contains(dl, "{") {
		return strings.TrimSuffix(dl, "/") + "/" + name + "/" + version + "/download", nil
	}

	if strings.Contains(dl, "{sha256-checksum}") {
		return "", fmt.Errorf("%w: {sha256-checksum} download URLs are not supported", repository.ErrUpstream)
	}

	return markers.Replace(dl), nil
}

func (s *Service) remoteFile(ctx context.Context, r repository.Repository, upstream, cachePath string) ([]byte, error) {
	item, err := s.repos.ResolveURL(ctx, r, upstream, cachePath)
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

	return io.ReadAll(io.LimitReader(rc, 64<<20))
}

// SetYanked yanks or unyanks a local crate version.
func (s *Service) SetYanked(ctx context.Context, repoKey, name, version string, yanked bool) error {
	versions, err := s.localVersions(ctx, repoKey, strings.ToLower(name))
	if err != nil {
		return err
	}

	for _, v := range versions {
		if v.line.Vers != version {
			continue
		}

		v.line.Yanked = yanked

		line, err := json.Marshal(v.line)
		if err != nil {
			return fmt.Errorf("encode index line: %w", err)
		}

		return s.repos.SetProperties(ctx, v.item, repository.Properties{
			propIndex: {
				string(line),
			},
		}, false)
	}

	return ErrNotFound
}

// SearchHit is one `cargo search` result.
type SearchHit struct {
	Name       string `json:"name"`
	MaxVersion string `json:"max_version"`
}

// crateNames lists the local crates of repoKey whose name contains q.
func (s *Service) crateNames(ctx context.Context, repoKey, q string) ([]string, error) {
	dir, err := s.repos.Stat(ctx, repoKey, "crates")
	if errors.Is(err, repository.ErrNotFound) {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	kids, err := s.repos.Children(ctx, dir)
	if err != nil {
		return nil, err
	}

	var names []string

	for _, k := range kids {
		if k.IsFolder() && strings.Contains(strings.ToLower(k.Name), strings.ToLower(q)) {
			names = append(names, k.Name)
		}
	}

	return names, nil
}

func highestVersion(versions []localVersion) string {
	highest := slices.MaxFunc(versions, func(a, b localVersion) int {
		if order := semver.Compare("v"+a.line.Vers, "v"+b.line.Vers); order != 0 {
			return order
		}

		return a.item.Created.Compare(b.item.Created)
	})

	return highest.line.Vers
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

func (s *Service) virtualIndex(ctx context.Context, r repository.Repository, name string) ([]byte, error) {
	var out bytes.Buffer

	seen := map[string]bool{}

	var upstreamErr error

	for _, m := range s.members(ctx, r, 0) {
		body, err := s.Index(ctx, m, name)
		switch {
		case errors.Is(err, ErrNotFound):
			continue
		case errors.Is(err, repository.ErrUpstream):
			upstreamErr = err
			continue
		case err != nil:
			return nil, err
		}

		for line := range bytes.SplitSeq(body, []byte("\n")) {
			var v struct {
				Vers string `json:"vers"`
			}
			if json.Unmarshal(line, &v) != nil || seen[v.Vers] {
				continue
			}

			seen[v.Vers] = true

			out.Write(line)
			out.WriteByte('\n')
		}
	}

	if out.Len() == 0 {
		if upstreamErr != nil {
			return nil, upstreamErr
		}

		return nil, ErrNotFound
	}

	return out.Bytes(), nil
}

func (s *Service) virtualCrate(
	ctx context.Context,
	r repository.Repository,
	name, version string,
) (repository.Item, error) {
	var upstreamErr error

	for _, m := range s.members(ctx, r, 0) {
		item, err := s.Crate(ctx, m, name, version)
		switch {
		case err == nil:
			return item, nil
		case errors.Is(err, repository.ErrUpstream):
			upstreamErr = err
		case !errors.Is(err, ErrNotFound) && !errors.Is(err, repository.ErrNotFound):
			return repository.Item{}, err
		}
	}

	if upstreamErr != nil {
		return repository.Item{}, upstreamErr
	}

	return repository.Item{}, ErrNotFound
}

// SearchRepositories finds the crates whose name contains q in the local
// members of r (r itself when local): the first limit of them, and how
// many there are. Only the returned crates have their versions read.
func (s *Service) SearchRepositories(
	ctx context.Context,
	r repository.Repository,
	q string,
	limit int,
) ([]SearchHit, int, error) {
	hits := []SearchHit{}
	seen := map[string]bool{}

	for _, m := range s.members(ctx, r, 0) {
		if m.Class != "local" {
			continue
		}

		names, err := s.crateNames(ctx, m.Name, q)
		if err != nil {
			return nil, 0, err
		}

		for _, name := range names {
			if seen[name] {
				continue
			}

			if len(hits) < limit {
				versions, err := s.localVersions(ctx, m.Name, strings.ToLower(name))
				if err != nil || len(versions) == 0 {
					continue
				}

				hits = append(hits, SearchHit{
					Name:       name,
					MaxVersion: highestVersion(versions),
				})
			}

			seen[name] = true
		}
	}

	return hits, len(seen), nil
}
