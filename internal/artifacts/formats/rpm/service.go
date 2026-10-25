package rpm

import (
	"compress/bzip2"
	"compress/gzip"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"

	"relic/internal/artifacts/formats/indexcache"
	"relic/internal/artifacts/repository"
	"relic/internal/artifacts/signingkey"
	"relic/internal/platform/telemetry"
)

// Format is the repository format this package serves.
const Format = repository.FormatRPM

// RepodataFolder is the folder that holds a repository's metadata.
const RepodataFolder = "repodata"

// The files served next to repomd.xml: its signature and the public key
// that checks it.
const (
	RepoMDSignature = "repomd.xml.asc"
	RepoMDKey       = "repomd.xml.key"
)

// Properties set on each indexed package.
const (
	PropName     = "rpm.name"
	PropVersion  = "rpm.version"
	PropArch     = "rpm.arch"
	PropMetadata = "rpm.metadata"
)

const maxRepoMDBytes = 1 << 20

// Service indexes and serves RPM repositories.
type Service struct {
	repos   *repository.Service
	keys    *signingkey.Service
	log     *slog.Logger
	now     func() time.Time
	indexes *indexcache.Cache[Repodata]
}

// NewService returns a Service; it indexes .rpm files deployed to RPM
// repositories, and remotes refresh their repodata like other metadata.
func NewService(repos *repository.Service, keySvc *signingkey.Service, log *slog.Logger) *Service {
	s := &Service{
		repos: repos,
		keys:  keySvc,
		log:   log,
		now:   time.Now,
	}
	s.indexes = indexcache.New[Repodata](string(Format), indexcache.DefaultSettings, func() time.Time {
		return s.now()
	})
	repos.RegisterHook(Format, s.indexPackage)
	repos.RegisterMetadata(isRepodataFile)

	return s
}

func isRepodataFile(name string) bool {
	if strings.HasPrefix(name, RepoMD) {
		return true
	}

	for _, kind := range []string{
		"primary.",
		"filelists.",
		"other.",
		"comps",
		"updateinfo.",
		"modules.",
	} {
		if strings.Contains(name, kind) && (strings.Contains(name, ".xml") || strings.Contains(name, ".yaml")) {
			return true
		}
	}

	return false
}

func (s *Service) indexPackage(ctx context.Context, r repository.Repository, item repository.Item) (err error) {
	if r.Class == "remote" || !strings.HasSuffix(item.Name, ".rpm") {
		return nil
	}

	ctx, span := telemetry.Tracer().Start(ctx, "rpm.Index")
	defer func() {
		telemetry.End(span, err)
	}()

	rc, err := s.repos.Open(ctx, item, nil)
	if err != nil {
		return err
	}

	defer func() {
		_ = rc.Close()
	}()

	pkg, err := Parse(rc)
	if err != nil {
		s.log.WarnContext(ctx, "not indexing an invalid rpm", "repo", r.Name, "path", item.Path(), "error", err)
		return nil
	}

	metadata, err := json.Marshal(pkg)
	if err != nil {
		return err
	}

	return s.repos.SetProperties(ctx, item, repository.Properties{
		PropName: {
			pkg.Name,
		},
		PropVersion: {
			pkg.EVR(),
		},
		PropArch: {
			pkg.Arch,
		},
		PropMetadata: {
			string(metadata),
		},
	}, false)
}

// ServesRepodata reports whether r generates repodata in folder root: a
// local repository at its metadata depth, a virtual one at its root.
func ServesRepodata(r repository.Repository, root string) bool {
	switch {
	case r.Format != Format:
		return false
	case r.Class == "virtual":
		return root == ""
	case r.Class == "remote":
		return false
	}

	depth := 0
	if root != "" {
		depth = strings.Count(root, "/") + 1
	}

	return depth == int(r.Config.MetadataDepth)
}

// Repodata returns the repodata of folder root in r: the packages below it,
// or for a virtual repository its members' packages and remote members'
// upstream repodata.
func (s *Service) Repodata(ctx context.Context, r repository.Repository, root string) (Repodata, error) {
	if r.Class != repository.ClassVirtual || repository.Filtered(ctx) {
		return s.buildRepodata(ctx, r, root)
	}

	return s.indexes.Get(ctx, r.Name+"\x00"+root, func(ctx context.Context) (Repodata, error) {
		return s.buildRepodata(ctx, r, root)
	})
}

func (s *Service) buildRepodata(ctx context.Context, r repository.Repository, root string) (files Repodata, err error) {
	ctx, span := telemetry.Tracer().Start(ctx, "rpm.Repodata")
	started := time.Now()

	defer func() {
		telemetry.End(span, err)
		telemetry.IndexBuilt(ctx, string(Format), string(r.Class), err, time.Since(started))
	}()

	var (
		entries   []Entry
		upstreams []UpstreamMetadata
		modified  time.Time
	)

	seen := map[string]bool{}

	for _, m := range s.members(ctx, r, 0) {
		if m.Class == "remote" {
			u, changed, ok, err := s.upstream(ctx, m)
			if err != nil {
				s.log.WarnContext(ctx, "skipping member repodata", "repo", r.Name, "member", m.Name, "error", err)
				continue
			}

			if ok {
				upstreams = append(upstreams, u)
				modified = latest(modified, changed)
			}

			continue
		}

		memberRoot := root
		if m.Name != r.Name {
			memberRoot = ""
		}

		local, changed, err := s.entries(ctx, m, memberRoot)
		if err != nil {
			return nil, err
		}

		modified = latest(modified, changed)

		for _, e := range local {
			if !seen[e.SHA256] {
				seen[e.SHA256] = true

				entries = append(entries, e)
			}
		}
	}

	return Build(entries, upstreams, modified)
}

func latest(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}

	return a
}

func (s *Service) entries(ctx context.Context, r repository.Repository, root string) ([]Entry, time.Time, error) {
	files, err := s.repos.FilesWithPropertyKey(ctx, r.Name, PropMetadata)
	if err != nil {
		return nil, time.Time{}, err
	}

	var (
		out      []Entry
		modified time.Time
	)

	for _, f := range files {
		location := f.Item.Path()
		if root != "" {
			rest, ok := strings.CutPrefix(location, root+"/")
			if !ok {
				continue
			}

			location = rest
		}

		var p Package
		if err := json.Unmarshal([]byte(f.Value), &p); err != nil {
			s.log.WarnContext(
				ctx,
				"skipping an unreadable rpm index",
				"repo",
				r.Name,
				"path",
				f.Item.Path(),
				"error",
				err,
			)

			continue
		}

		out = append(out, Entry{
			Package:  p,
			Location: location,
			Size:     f.Item.Size,
			SHA256:   f.Item.Checksums.SHA256,
			Modified: f.Item.Modified.Unix(),
		})
		modified = latest(modified, f.Item.Modified)
	}

	return out, modified, nil
}

type repoMDData struct {
	Data []struct {
		Type     string `xml:"type,attr"`
		Location struct {
			Href string `xml:"href,attr"`
		} `xml:"location"`
	} `xml:"data"`
}

func (s *Service) upstream(ctx context.Context, m repository.Repository) (UpstreamMetadata, time.Time, bool, error) {
	if repository.Restricted(ctx, m.Name) {
		return UpstreamMetadata{}, time.Time{}, false, nil
	}

	item, err := s.repos.Resolve(ctx, m.Name, path.Join(RepodataFolder, RepoMD))
	if errors.Is(err, repository.ErrNotFound) {
		return UpstreamMetadata{}, time.Time{}, false, nil
	}

	if err != nil {
		return UpstreamMetadata{}, time.Time{}, false, err
	}

	raw, err := s.read(ctx, item, maxRepoMDBytes)
	if err != nil {
		return UpstreamMetadata{}, time.Time{}, false, err
	}

	var md repoMDData
	if err := xml.Unmarshal(raw, &md); err != nil {
		return UpstreamMetadata{}, time.Time{}, false, fmt.Errorf("upstream %s: %w", RepoMD, err)
	}

	u := UpstreamMetadata{}

	for _, d := range md.Data {
		open := s.opener(ctx, m.Name, d.Location.Href)

		switch d.Type {
		case "primary":
			u.Primary = open
		case "filelists":
			u.Filelists = open
		case "other":
			u.Other = open
		}
	}

	if u.Primary == nil {
		return UpstreamMetadata{}, time.Time{}, false, fmt.Errorf("upstream %s lists no primary metadata", RepoMD)
	}

	return u, item.Modified, true, nil
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

func (s *Service) opener(ctx context.Context, repoKey, href string) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) {
		item, err := s.repos.Resolve(ctx, repoKey, href)
		if err != nil {
			return nil, err
		}

		rc, err := s.repos.Open(ctx, item, nil)
		if err != nil {
			return nil, err
		}

		plain, err := decompressed(href, rc)
		if err != nil {
			_ = rc.Close()
			return nil, fmt.Errorf("%s: %w", href, err)
		}

		return plain, nil
	}
}

type decompressedReader struct {
	io.Reader
	closers []func() error
}

func (d decompressedReader) Close() error {
	errs := make([]error, 0, len(d.closers))
	for _, c := range d.closers {
		errs = append(errs, c())
	}

	return errors.Join(errs...)
}

func decompressed(name string, rc io.ReadCloser) (io.ReadCloser, error) {
	switch path.Ext(name) {
	case ".gz":
		zr, err := gzip.NewReader(rc)
		if err != nil {
			return nil, err
		}

		return decompressedReader{
			Reader: zr,
			closers: []func() error{
				zr.Close,
				rc.Close,
			},
		}, nil
	case ".xz":
		xr, err := xz.NewReader(rc)
		if err != nil {
			return nil, err
		}

		return decompressedReader{
			Reader: xr,
			closers: []func() error{
				rc.Close,
			},
		}, nil
	case ".zst":
		zr, err := zstd.NewReader(rc)
		if err != nil {
			return nil, err
		}

		return decompressedReader{
			Reader: zr,
			closers: []func() error{
				func() error {
					zr.Close()
					return nil
				},
				rc.Close,
			},
		}, nil
	case ".bz2":
		return decompressedReader{
			Reader: bzip2.NewReader(rc),
			closers: []func() error{
				rc.Close,
			},
		}, nil
	}

	return rc, nil
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
		if m, err := s.repos.GetRepository(ctx, key); err == nil && m.Format == Format {
			out = append(out, s.members(ctx, m, depth+1)...)
		}
	}

	return out
}

// Sign returns repomd.xml.asc: repomd's detached signature with r's key
// pair.
func (s *Service) Sign(ctx context.Context, r repository.Repository, repoMD []byte) ([]byte, error) {
	key, _, err := s.keys.GPG(ctx, r.KeyPairRef())
	if err != nil {
		return nil, err
	}

	return signingkey.DetachSign(repoMD, key)
}

// PublicKey returns the armored public key that checks r's repomd.xml.asc.
func (s *Service) PublicKey(ctx context.Context, r repository.Repository) (string, error) {
	_, public, err := s.keys.GPG(ctx, r.KeyPairRef())

	return public, err
}
