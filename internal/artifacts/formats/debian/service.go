package debian

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"slices"
	"strings"
	"time"

	"relic/internal/artifacts/formats/indexcache"
	"relic/internal/artifacts/repository"
	"relic/internal/artifacts/signingkey"
	"relic/internal/platform/database"
	"relic/internal/platform/telemetry"
)

// Format is the repository format this package serves.
const Format = repository.FormatDebian

// Properties that place a package (uploads set the first three).
const (
	PropDistribution = "deb.distribution"
	PropComponent    = "deb.component"
	PropArchitecture = "deb.architecture"
	PropControl      = "deb.control"
	PropName         = "deb.name"
	PropVersion      = "deb.version"
)

var defaultArchitectures = []string{
	"amd64",
	"arm64",
}

// Service indexes and serves Debian repositories.
type Service struct {
	db      *database.DB
	repos   *repository.Service
	keys    *signingkey.Service
	log     *slog.Logger
	now     func() time.Time
	indexes *indexcache.Cache[*Dist]
}

// NewService returns a Service; it reads the control file of .deb files
// deployed to Debian repositories, and remotes refresh dists/ like other
// metadata.
func NewService(db *database.DB, repos *repository.Service, keySvc *signingkey.Service, log *slog.Logger) *Service {
	s := &Service{
		db:    db,
		repos: repos,
		keys:  keySvc,
		log:   log,
		now:   time.Now,
	}
	s.indexes = indexcache.New[*Dist](string(Format), indexcache.DefaultSettings, func() time.Time {
		return s.now()
	})
	repos.RegisterHook(Format, s.indexPackage)
	repos.RegisterMetadata(func(name string) bool {
		return slices.Contains([]string{
			"InRelease",
			"Release",
			"Release.gpg",
		}, name) ||
			strings.HasPrefix(name, "Packages")
	})

	return s
}

func (s *Service) indexPackage(ctx context.Context, r repository.Repository, item repository.Item) error {
	if r.Class == "remote" || !strings.HasSuffix(item.Name, ".deb") {
		return nil
	}

	rc, err := s.repos.Open(ctx, item, nil)
	if err != nil {
		return err
	}

	defer func() {
		_ = rc.Close()
	}()

	ctl, err := Control(rc)
	if err != nil {
		s.log.WarnContext(ctx, "not indexing an invalid deb", "repo", r.Name, "path", item.Path(), "error", err)
		return nil
	}

	props := repository.Properties{
		PropControl: {
			ctl,
		},
		PropName: {
			Field(ctl, "Package"),
		},
		PropVersion: {
			Field(ctl, "Version"),
		},
	}

	existing, err := s.repos.Properties(ctx, item)
	if err != nil {
		return err
	}

	if len(existing[PropArchitecture]) == 0 {
		props[PropArchitecture] = []string{
			Field(ctl, "Architecture"),
		}
	}

	return s.repos.SetProperties(ctx, item, props, false)
}

// Dist is a distribution's generated indexes.
type Dist struct {
	Components, Architectures []string
	Files                     map[string][]byte
	Release                   []byte
}

// Distribution returns dist's indexes for r: its own packages, or for a
// virtual repository its members' (remote members' upstream indexes,
// limited to the configured architectures).
func (s *Service) Distribution(ctx context.Context, r repository.Repository, dist string) (*Dist, error) {
	if r.Class != repository.ClassVirtual || repository.AnyRestricted(ctx, s.members(ctx, r, 0)) {
		return s.buildDistribution(ctx, r, dist)
	}

	return s.indexes.Get(repository.Unfiltered(ctx), r.Name+"\x00"+dist, func(ctx context.Context) (*Dist, error) {
		return s.buildDistribution(ctx, r, dist)
	})
}

func (s *Service) buildDistribution(ctx context.Context, r repository.Repository, dist string) (idx *Dist, err error) {
	ctx, span := telemetry.Tracer().Start(ctx, "debian.Distribution")
	started := time.Now()

	defer func() {
		telemetry.End(span, err)
		telemetry.IndexBuilt(ctx, string(Format), string(r.Class), err, time.Since(started))
	}()

	paras := map[string]map[string][]string{}
	seen := map[string]bool{}
	add := func(comp, arch, p string) {
		id := comp + "\x00" + arch + "\x00" + Field(
			p,
			"Package",
		) + "\x00" + Field(
			p,
			"Version",
		) + "\x00" + Field(
			p,
			"Architecture",
		)
		if seen[id] {
			return
		}

		seen[id] = true

		if paras[comp] == nil {
			paras[comp] = map[string][]string{}
		}

		paras[comp][arch] = append(paras[comp][arch], p)
	}
	found := false

	var (
		memberErr error
		modified  time.Time
	)

	for _, m := range s.members(ctx, r, 0) {
		var (
			ok      bool
			changed time.Time
			err     error
		)
		if m.Class == "remote" {
			ok, changed, err = s.remoteParagraphs(ctx, m, dist, architectures(r), add)
		} else {
			ok, changed, err = s.localParagraphs(ctx, m, dist, add)
		}

		if changed.After(modified) {
			modified = changed
		}

		if err != nil {
			s.log.WarnContext(
				ctx,
				"skipping member distribution",
				"repo",
				r.Name,
				"member",
				m.Name,
				"dist",
				dist,
				"error",
				err,
			)

			memberErr = err

			continue
		}

		found = found || ok
	}

	if !found && memberErr != nil {
		return nil, fmt.Errorf("distribution %s: %w", dist, memberErr)
	}

	if !found {
		return nil, repository.ErrNotFound
	}

	idx, release, err := assemble(r.Name, dist, paras)
	if err != nil {
		return nil, err
	}

	digest := sha256.Sum256(release(time.Time{}))

	date, err := s.releaseDate(ctx, r.Name, dist, hex.EncodeToString(digest[:]), modified)
	if err != nil {
		return nil, err
	}

	idx.Release = release(date)

	return idx, nil
}

func (s *Service) releaseDate(
	ctx context.Context,
	repoKey, dist, digest string,
	modified time.Time,
) (time.Time, error) {
	stored, err := s.db.GetDebianReleaseDate(ctx, database.GetDebianReleaseDateParams{
		RepoKey:      repoKey,
		Distribution: dist,
	})

	found := err == nil
	if err != nil && !errors.Is(database.NotFound(err), database.ErrNotFound) {
		return time.Time{}, fmt.Errorf("release date of %s %s: %w", repoKey, dist, err)
	}

	switch {
	case found && stored.Digest == digest:
		return stored.Date, nil
	case repository.Filtered(ctx) && found && stored.Date.After(modified):
		return stored.Date, nil
	case repository.Filtered(ctx):
		return modified, nil
	}

	date := s.now().UTC().Truncate(time.Second)
	if found && !date.After(stored.Date) {
		date = stored.Date.Add(time.Second)
	}

	if err := s.db.PutDebianReleaseDate(ctx, database.PutDebianReleaseDateParams{
		RepoKey:      repoKey,
		Distribution: dist,
		Digest:       digest,
		Date:         date,
	}); err != nil {
		return time.Time{}, fmt.Errorf("release date of %s %s: %w", repoKey, dist, err)
	}

	return date, nil
}

func assemble(label, dist string, paras map[string]map[string][]string) (*Dist, func(time.Time) []byte, error) {
	comps, archSet := map[string]bool{}, map[string]bool{}
	for comp, byArch := range paras {
		comps[comp] = true

		for arch := range byArch {
			if arch != "all" {
				archSet[arch] = true
			}
		}
	}

	archs := sortedKeys(archSet)
	d := &Dist{
		Components:    sortedKeys(comps),
		Architectures: archs,
		Files:         map[string][]byte{},
	}

	var files []IndexFile

	for _, comp := range d.Components {
		list := append(slices.Clone(archs), "all")
		for _, arch := range list {
			ps := paras[comp][arch]
			if arch != "all" {
				ps = append(slices.Clone(ps), paras[comp]["all"]...)
			} else if len(ps) == 0 {
				continue
			}

			data := Packages(ps)

			gz, err := Gzip(data)
			if err != nil {
				return nil, nil, err
			}

			base := comp + "/binary-" + arch + "/Packages"
			d.Files[base], d.Files[base+".gz"] = data, gz
			files = append(files, IndexFile{
				base,
				data,
			}, IndexFile{
				base + ".gz",
				gz,
			})
		}
	}

	if len(archs) == 0 {
		archs = []string{
			"all",
		}
	}

	return d, func(date time.Time) []byte {
		return Release(label, dist, d.Components, archs, files, date)
	}, nil
}

func (s *Service) localParagraphs(
	ctx context.Context,
	m repository.Repository,
	dist string,
	add func(comp, arch, p string),
) (bool, time.Time, error) {
	items, err := s.repos.FilesWithProperty(ctx, m.Name, PropDistribution, dist)
	if err != nil {
		return false, time.Time{}, err
	}

	var modified time.Time

	for _, it := range items {
		props, err := s.repos.Properties(ctx, it)
		if err != nil {
			return false, time.Time{}, err
		}

		if it.Modified.After(modified) {
			modified = it.Modified
		}

		ctl := first(props[PropControl])
		if ctl == "" {
			continue
		}

		components := listedValues(props[PropComponent])
		if len(components) == 0 {
			components = []string{
				"main",
			}
		}

		archs := listedValues(props[PropArchitecture])
		if len(archs) == 0 {
			archs = []string{
				Field(ctl, "Architecture"),
			}
		}

		paragraph := File{
			Control:  ctl,
			Filename: it.Path(),
			Size:     it.Size,
			MD5:      it.Checksums.MD5,
			SHA1:     it.Checksums.SHA1,
			SHA256:   it.Checksums.SHA256,
		}.Paragraph()

		for _, component := range components {
			for _, arch := range archs {
				add(component, arch, paragraph)
			}
		}
	}

	return len(items) > 0, modified, nil
}

func (s *Service) remoteParagraphs(
	ctx context.Context,
	m repository.Repository,
	dist string,
	archs []string,
	add func(comp, arch, p string),
) (bool, time.Time, error) {
	releaseItem, err := s.repos.Resolve(ctx, m.Name, path.Join("dists", dist, "Release"))
	if errors.Is(err, repository.ErrNotFound) {
		return false, time.Time{}, nil
	}

	if err != nil {
		return false, time.Time{}, err
	}

	rel, err := s.read(ctx, m.Name, path.Join("dists", dist, "Release"))
	if errors.Is(err, repository.ErrNotFound) {
		return false, time.Time{}, nil
	}

	if err != nil {
		return false, time.Time{}, err
	}

	upstreamArchs := strings.Fields(Field(string(rel), "Architectures"))
	for comp := range strings.FieldsSeq(Field(string(rel), "Components")) {
		for _, arch := range append(slices.Clone(archs), "all") {
			if arch != "all" && !slices.Contains(upstreamArchs, arch) {
				continue
			}

			for _, name := range []string{
				"Packages.xz",
				"Packages.gz",
				"Packages",
			} {
				data, err := s.read(ctx, m.Name, path.Join("dists", dist, comp, "binary-"+arch, name))
				if errors.Is(err, repository.ErrNotFound) {
					continue
				}

				if err != nil {
					return false, time.Time{}, err
				}

				plain, err := ReadIndex(name, strings.NewReader(string(data)))
				if err != nil {
					return false, time.Time{}, fmt.Errorf("%s: %w", name, err)
				}

				restricted := repository.Restricted(ctx, m.Name)
				for _, p := range Paragraphs(plain) {
					if restricted && !repository.Visible(ctx, m.Name, Field(p, "Filename")) {
						continue
					}

					add(comp, arch, p)
				}

				break
			}
		}
	}

	return true, releaseItem.Modified, nil
}

func (s *Service) read(ctx context.Context, repoKey, p string) ([]byte, error) {
	item, err := s.repos.Resolve(ctx, repoKey, p)
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

	return readCapped(rc, 256<<20)
}

func architectures(r repository.Repository) []string {
	if len(r.Config.Architectures) > 0 {
		return r.Config.Architectures
	}

	return defaultArchitectures
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

func listedValues(values []string) []string {
	var out []string

	for _, v := range values {
		for part := range strings.SplitSeq(v, ",") {
			if part = strings.TrimSpace(part); part != "" && !slices.Contains(out, part) {
				out = append(out, part)
			}
		}
	}

	return out
}

func first(v []string) string {
	if len(v) == 0 {
		return ""
	}

	return v[0]
}

// Sign returns InRelease (clear-signed) or Release.gpg (detached) for a
// Release, with each of r's key pairs.
func (s *Service) Sign(ctx context.Context, r repository.Repository, release []byte, detached bool) ([]byte, error) {
	keys, _, err := s.keys.GPGKeys(ctx, r.KeyPairRefs()...)
	if err != nil {
		return nil, err
	}

	if detached {
		return signingkey.DetachSign(release, keys...)
	}

	return signingkey.ClearSign(release, keys...)
}
