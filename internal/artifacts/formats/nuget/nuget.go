// Package nuget implements NuGet feeds on Relic repositories: the V3 protocol (service index, flat container, registrations,
// search) and package push, for local, remote and virtual repositories.
package nuget

import (
	"archive/zip"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"relic/internal/artifacts/repository"

	"golang.org/x/mod/semver"
)

// Format is the repository format this package serves.
const Format = repository.FormatNuGet

// Errors mapped by the API layer.
var (
	ErrNotFound = errors.New("package not found")
	ErrInvalid  = errors.New("invalid package")
	ErrExists   = errors.New("package version already exists")
)

const maxNuspec = 4 << 20

var (
	idRe      = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,99}$`)
	versionRe = regexp.MustCompile(`^[0-9]+(\.[0-9]+){1,3}(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)
)

// ValidID reports whether id is a valid package id.
func ValidID(id string) bool { return idRe.MatchString(id) }

// ValidVersion reports whether v is a valid NuGet version.
func ValidVersion(v string) bool { return versionRe.MatchString(v) }

// Path is where a pushed package is stored in a local repository.
func Path(id, version string) string {
	return id + "." + normalizeVersion(version) + ".nupkg"
}

// Service implements feeds on top of repo.Service.
type Service struct {
	repos   *repository.Service
	nuspecs *nuspecCache
	log     *slog.Logger
}

// NewService returns a Service and registers its deploy hook, which sets
// nuget.id and nuget.version on .nupkg files deployed by any route.
func NewService(repos *repository.Service, log *slog.Logger) *Service {
	s := &Service{
		repos:   repos,
		nuspecs: newNuspecCache(nuspecCacheBytes),
		log:     log,
	}
	repos.RegisterHook(Format, s.index)
	repos.RegisterMetadata(func(name string) bool {
		return name == "index.json"
	})

	return s
}

// Nuspec is the part of a package manifest Relic reads.
type Nuspec struct {
	Metadata struct {
		ID           string `xml:"id"`
		Version      string `xml:"version"`
		Description  string `xml:"description"`
		Authors      string `xml:"authors"`
		PackageTypes struct {
			Types []struct {
				Name string `xml:"name,attr"`
			} `xml:"packageType"`
		} `xml:"packageTypes"`
		Dependencies struct {
			Groups []struct {
				TargetFramework string `xml:"targetFramework,attr"`
				Dependencies    []struct {
					ID      string `xml:"id,attr"`
					Version string `xml:"version,attr"`
				} `xml:"dependency"`
			} `xml:"group"`
			Flat []struct {
				ID      string `xml:"id,attr"`
				Version string `xml:"version,attr"`
			} `xml:"dependency"`
		} `xml:"dependencies"`
	} `xml:"metadata"`
}

// ReadNuspec reads the .nuspec at the root of a .nupkg (a zip archive).
func ReadNuspec(r io.ReaderAt, size int64) (Nuspec, []byte, error) {
	zr, err := zipArchive(r, size)
	if err != nil {
		return Nuspec{}, nil, err
	}

	for _, f := range zr.File {
		if strings.Contains(f.Name, "/") || !strings.HasSuffix(strings.ToLower(f.Name), ".nuspec") {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			return Nuspec{}, nil, err
		}

		raw, err := io.ReadAll(io.LimitReader(rc, maxNuspec))
		_ = rc.Close()

		if err != nil {
			return Nuspec{}, nil, err
		}

		var n Nuspec
		if err := xml.Unmarshal(raw, &n); err != nil || !ValidID(n.Metadata.ID) || !ValidVersion(n.Metadata.Version) {
			return Nuspec{}, nil, fmt.Errorf("%w: unreadable .nuspec", ErrInvalid)
		}

		return n, raw, nil
	}

	return Nuspec{}, nil, fmt.Errorf("%w: no .nuspec", ErrInvalid)
}

func zipArchive(r io.ReaderAt, size int64) (*zip.Reader, error) {
	zr, err := zip.NewReader(r, size)
	if errors.Is(err, zip.ErrFormat) || errors.Is(err, zip.ErrAlgorithm) || errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, fmt.Errorf("%w: not a zip archive", ErrInvalid)
	}

	if err != nil {
		return nil, fmt.Errorf("read the archive: %w", err)
	}

	return zr, nil
}

// Push stores a pushed package (a .nupkg spooled to file) at its layout
// path, and a symbol package (.snupkg) at SymbolsPath with the symbol server
// keys of its portable PDBs.
func (s *Service) Push(ctx context.Context, repoKey string, file *os.File, user string) (Nuspec, error) {
	info, err := file.Stat()
	if err != nil {
		return Nuspec{}, err
	}

	n, _, err := ReadNuspec(file, info.Size())
	if err != nil {
		return Nuspec{}, err
	}

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return Nuspec{}, err
	}

	if n.SymbolsPackage() {
		return n, s.pushSymbols(ctx, repoKey, n, file, user)
	}

	id, version := n.Metadata.ID, normalizeVersion(n.Metadata.Version)
	lock := "nuget-push:" + repoKey + "\x00" + strings.ToLower(id) + "\x00" + strings.ToLower(version)

	err = s.repos.WithLock(ctx, lock, func() error {
		if _, err := s.localPackage(ctx, repoKey, id, version); err == nil {
			return fmt.Errorf("%w: %s %s", ErrExists, id, version)
		}

		_, err := s.repos.Deploy(ctx, repository.DeployInput{
			Repository: repoKey,
			Path:       Path(id, version),
			Body:       file,
			User:       user,
			Props: repository.Properties{
				"nuget.id": {
					id,
				},
				"nuget.version": {
					version,
				},
			},
		})

		return err
	})

	return n, err
}

func (s *Service) index(ctx context.Context, r repository.Repository, item repository.Item) error {
	if r.Class != "local" || !strings.HasSuffix(strings.ToLower(item.Name), ".nupkg") {
		return nil
	}

	props, err := s.repos.Properties(ctx, item)
	if err != nil || len(props["nuget.id"]) > 0 {
		return err
	}

	n, _, err := ReadNuspec(s.repos.ReaderAt(ctx, item), item.Size)
	if err != nil {
		return err
	}

	return s.repos.SetProperties(ctx, item, repository.Properties{
		"nuget.id": {
			n.Metadata.ID,
		},
		"nuget.version": {
			normalizeVersion(n.Metadata.Version),
		},
	}, false)
}

func normalizeVersion(v string) string {
	v, _, _ = strings.Cut(v, "+")
	core, pre, hasPre := strings.Cut(v, "-")

	parts := strings.Split(core, ".")
	if len(parts) == 4 && parts[3] == "0" {
		parts = parts[:3]
	}

	for len(parts) < 3 {
		parts = append(parts, "0")
	}

	out := strings.Join(parts, ".")
	if hasPre {
		out += "-" + pre
	}

	return out
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

// Versions lists a package's versions (normalized, lower-cased) across r's
// members, sorted.
func (s *Service) Versions(ctx context.Context, r repository.Repository, id string) ([]string, error) {
	lower := strings.ToLower(id)
	seen := map[string]bool{}

	var (
		out         []string
		upstreamErr error
	)

	for _, m := range s.members(ctx, r, 0) {
		var (
			vs  []string
			err error
		)
		if m.Class == "remote" {
			vs, err = s.remoteVersions(ctx, m, lower)
		} else {
			vs, err = s.localVersions(ctx, m.Name, id)
		}

		if errors.Is(err, repository.ErrUpstream) {
			upstreamErr = err
			continue
		}

		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, err
		}

		for _, v := range vs {
			if lv := strings.ToLower(v); !seen[lv] {
				seen[lv] = true
				out = append(out, lv)
			}
		}
	}

	if len(out) == 0 {
		if upstreamErr != nil {
			return nil, upstreamErr
		}

		return nil, ErrNotFound
	}

	slices.SortFunc(out, compareVersions)

	return out, nil
}

func (s *Service) localPackages(ctx context.Context, repoKey, id string) (map[string]repository.Item, error) {
	items, err := s.repos.FilesWithProperty(ctx, repoKey, "nuget.id", id)
	if err != nil {
		return nil, err
	}

	out := map[string]repository.Item{}

	for _, it := range items {
		lower := strings.ToLower(it.Name)

		v, ok := strings.CutSuffix(strings.TrimPrefix(lower, strings.ToLower(id)+"."), ".nupkg")
		if !ok || !ValidVersion(v) {
			v, ok = s.propertyVersion(ctx, it)
		}

		if ok && ValidVersion(v) {
			out[v] = it
		}
	}

	if len(out) == 0 {
		return nil, ErrNotFound
	}

	return out, nil
}

func (s *Service) propertyVersion(ctx context.Context, item repository.Item) (string, bool) {
	props, err := s.repos.Properties(ctx, item)
	if err != nil || len(props["nuget.version"]) == 0 {
		return "", false
	}

	return strings.ToLower(props["nuget.version"][0]), true
}

func (s *Service) localVersions(ctx context.Context, repoKey, id string) ([]string, error) {
	pkgs, err := s.localPackages(ctx, repoKey, id)
	if err != nil {
		return nil, err
	}

	out := make([]string, 0, len(pkgs))
	for v := range pkgs {
		out = append(out, v)
	}

	return out, nil
}

// Package finds the .nupkg of id and version through r's members.
func (s *Service) Package(ctx context.Context, r repository.Repository, id, version string) (repository.Item, error) {
	var upstreamErr error

	for _, m := range s.members(ctx, r, 0) {
		var (
			it  repository.Item
			err error
		)
		if m.Class == "remote" {
			it, err = s.remotePackage(ctx, m, strings.ToLower(id), strings.ToLower(version))
		} else {
			it, err = s.localPackage(ctx, m.Name, id, version)
		}

		switch {
		case err == nil:
			return it, nil
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

func (s *Service) localPackage(ctx context.Context, repoKey, id, version string) (repository.Item, error) {
	pkgs, err := s.localPackages(ctx, repoKey, id)
	if err != nil {
		return repository.Item{}, err
	}

	it, ok := pkgs[strings.ToLower(normalizeVersion(version))]
	if !ok {
		return repository.Item{}, ErrNotFound
	}

	return it, nil
}

func (s *Service) remoteBase(r repository.Repository) string {
	if u := r.Upstream().IndexURL; u != "" && strings.Contains(u, "flatcontainer") {
		return strings.TrimSuffix(u, "/")
	}

	u := strings.TrimSuffix(s.repos.UpstreamOf(r).URL, "/")
	if strings.Contains(u, "nuget.org") {
		return "https://api.nuget.org/v3-flatcontainer"
	}

	return u + "/flatcontainer"
}

func (s *Service) remoteVersions(ctx context.Context, r repository.Repository, lower string) ([]string, error) {
	item, err := s.repos.ResolveURL(ctx, r, s.remoteBase(r)+"/"+lower+"/index.json", ".nuget/"+lower+"/index.json")
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

	var doc struct {
		Versions []string `json:"versions"`
	}
	if err := json.NewDecoder(io.LimitReader(rc, 16<<20)).Decode(&doc); err != nil {
		return nil, fmt.Errorf("%w: versions of %s: %w", repository.ErrUpstream, lower, err)
	}

	if !repository.Restricted(ctx, r.Name) {
		return doc.Versions, nil
	}

	visible := doc.Versions[:0]
	for _, v := range doc.Versions {
		if repository.Visible(ctx, r.Name, lower+"."+strings.ToLower(v)+".nupkg") {
			visible = append(visible, v)
		}
	}

	if len(visible) == 0 {
		return nil, ErrNotFound
	}

	return visible, nil
}

func (s *Service) remotePackage(
	ctx context.Context,
	r repository.Repository,
	lower, version string,
) (repository.Item, error) {
	if pkgs, err := s.localPackages(ctx, r.Name, lower); err == nil {
		if it, ok := pkgs[strings.ToLower(normalizeVersion(version))]; ok {
			return it, nil
		}
	}

	file := lower + "." + version + ".nupkg"

	fetched, err := s.repos.ResolveURL(ctx, r, s.remoteBase(r)+"/"+lower+"/"+version+"/"+file, lower+"/"+file)
	if err != nil {
		return repository.Item{}, err
	}

	return s.adopt(ctx, r, fetched)
}

func (s *Service) adopt(
	ctx context.Context,
	r repository.Repository,
	fetched repository.Item,
) (repository.Item, error) {
	_, n, err := s.Nuspec(ctx, fetched)
	if err != nil || n.Metadata.ID == "" || n.Metadata.Version == "" {
		return repository.Item{}, fmt.Errorf(
			"%w: package %s has no readable nuspec: %w",
			repository.ErrUpstream,
			fetched.Name,
			err,
		)
	}

	name := n.Metadata.ID + "." + n.Metadata.Version + ".nupkg"
	if name == fetched.Path() {
		return fetched, nil
	}

	item, err := s.repos.CacheByChecksum(ctx, repository.DeployInput{
		Repository: r.Name,
		Path:       name,
		Expected:   fetched.Checksums,
		User:       fetched.CreatedBy,
		Props: repository.Properties{
			"nuget.id": {
				n.Metadata.ID,
			},
			"nuget.version": {
				normalizeVersion(n.Metadata.Version),
			},
		},
	})
	if err != nil {
		return repository.Item{}, fmt.Errorf("cache %s: %w", name, err)
	}

	if err := s.repos.Delete(ctx, r.Name, fetched.Path()); err != nil {
		s.log.WarnContext(ctx, "nuget: remove fetched package", "repo", r.Name, "path", fetched.Path(), "error", err)
	}

	return item, nil
}

// Nuspec returns a stored package's manifest.
func (s *Service) Nuspec(ctx context.Context, item repository.Item) ([]byte, Nuspec, error) {
	if cached, ok := s.nuspecs.get(item.Checksums.SHA256); ok {
		return cached.raw, cached.nuspec, nil
	}

	n, raw, err := ReadNuspec(s.repos.ReaderAt(ctx, item), item.Size)
	if err != nil {
		return nil, Nuspec{}, err
	}

	s.nuspecs.add(cachedNuspec{
		sha256: item.Checksums.SHA256,
		raw:    raw,
		nuspec: n,
	})

	return raw, n, nil
}

// Search lists packages whose id contains q, with their versions: those
// stored in r's members (a remote's cached ones too), then what each remote
// member's upstream search service finds for the first skip+take results.
func (s *Service) Search(
	ctx context.Context,
	r repository.Repository,
	q string,
	skip, take int,
) ([]SearchResult, int, error) {
	q = strings.ToLower(q)
	byID := map[string]*SearchResult{}
	upstreamTotal := 0

	for _, m := range s.members(ctx, r, 0) {
		if m.Class == "remote" {
			found, total, err := s.remoteSearch(ctx, m, q, skip+take)
			if err != nil {
				s.log.WarnContext(ctx, "nuget: upstream search", "repo", m.Name, "error", err)
			}

			upstreamTotal += total - len(found)

			for _, res := range found {
				addSearchResult(byID, res.ID, res.Versions...)
			}
		}

		root, err := s.repos.Stat(ctx, m.Name, "")
		if errors.Is(err, repository.ErrNotFound) && m.Class == "remote" {
			continue
		}

		if err != nil {
			return nil, 0, err
		}

		items, err := s.repos.Subtree(ctx, root)
		if err != nil {
			return nil, 0, err
		}

		for _, it := range items {
			if it.IsFolder() || !strings.HasSuffix(strings.ToLower(it.Name), ".nupkg") {
				continue
			}

			props, err := s.repos.Properties(ctx, it)
			if err != nil || len(props["nuget.id"]) == 0 || len(props["nuget.version"]) == 0 {
				continue
			}

			id, v := props["nuget.id"][0], strings.ToLower(props["nuget.version"][0])
			if !strings.Contains(strings.ToLower(id), q) {
				continue
			}

			addSearchResult(byID, id, v)
		}
	}

	out := make([]SearchResult, 0, len(byID))
	for _, r := range byID {
		slices.SortFunc(r.Versions, compareVersions)
		r.Versions = slices.Compact(r.Versions)
		out = append(out, *r)
	}

	slices.SortFunc(
		out,
		func(a, b SearchResult) int {
			return strings.Compare(strings.ToLower(a.ID), strings.ToLower(b.ID))
		},
	)
	total := len(out) + max(upstreamTotal, 0)
	skip = min(skip, len(out))

	out = out[skip:]
	if take > 0 && take < len(out) {
		out = out[:take]
	}

	return out, total, nil
}

func addSearchResult(byID map[string]*SearchResult, id string, versions ...string) {
	res, ok := byID[strings.ToLower(id)]
	if !ok {
		res = &SearchResult{
			ID: id,
		}
		byID[strings.ToLower(id)] = res
	}

	for _, v := range versions {
		res.Versions = append(res.Versions, strings.ToLower(v))
	}
}

const maxUpstreamSearchBytes = 16 << 20

func (s *Service) remoteSearch(
	ctx context.Context,
	r repository.Repository,
	q string,
	take int,
) ([]SearchResult, int, error) {
	if r.Upstream().Offline || take <= 0 {
		return nil, 0, nil
	}

	searchURL, err := s.searchService(ctx, r)
	if err != nil || searchURL == "" {
		return nil, 0, err
	}

	query := url.Values{
		"q": {
			q,
		},
		"skip": {
			"0",
		},
		"take": {
			strconv.Itoa(take),
		},
		"prerelease": {
			"true",
		},
		"semVerLevel": {
			"2.0.0",
		},
	}

	body, err := s.repos.FetchUpstream(ctx, r, searchURL+"?"+query.Encode(), "application/json", maxUpstreamSearchBytes)
	if err != nil {
		return nil, 0, fmt.Errorf("search %s: %w", r.Name, err)
	}

	var doc struct {
		TotalHits int `json:"totalHits"`
		Data      []struct {
			ID       string `json:"id"`
			Versions []struct {
				Version string `json:"version"`
			} `json:"versions"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, 0, fmt.Errorf("%w: search answer of %s: %w", repository.ErrUpstream, r.Name, err)
	}

	out := make([]SearchResult, 0, len(doc.Data))

	for _, d := range doc.Data {
		if !ValidID(d.ID) {
			continue
		}

		res := SearchResult{
			ID: d.ID,
		}

		for _, v := range d.Versions {
			lower := strings.ToLower(v.Version)
			if ValidVersion(lower) &&
				repository.Visible(ctx, r.Name, strings.ToLower(d.ID)+"."+normalizeVersion(lower)+".nupkg") {
				res.Versions = append(res.Versions, lower)
			}
		}

		if len(res.Versions) > 0 {
			out = append(out, res)
		}
	}

	return out, doc.TotalHits, nil
}

func (s *Service) searchService(ctx context.Context, r repository.Repository) (string, error) {
	index := strings.TrimSuffix(s.repos.UpstreamOf(r).URL, "/")
	switch {
	case strings.Contains(index, "nuget.org"):
		index = "https://api.nuget.org/v3/index.json"
	case !strings.HasSuffix(index, "/index.json"):
		index += "/index.json"
	}

	body, err := s.repos.FetchUpstream(ctx, r, index, "application/json", maxUpstreamSearchBytes)
	if errors.Is(err, repository.ErrNotFound) {
		return "", nil
	}

	if err != nil {
		return "", fmt.Errorf("service index of %s: %w", r.Name, err)
	}

	var doc struct {
		Resources []struct {
			ID   string `json:"@id"`
			Type string `json:"@type"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", fmt.Errorf("%w: service index of %s: %w", repository.ErrUpstream, r.Name, err)
	}

	for _, res := range doc.Resources {
		if strings.HasPrefix(res.Type, "SearchQueryService") &&
			(strings.HasPrefix(res.ID, "https://") || strings.HasPrefix(res.ID, "http://")) {
			return res.ID, nil
		}
	}

	return "", nil
}

// SearchResult is one package in search results.
type SearchResult struct {
	ID       string
	Versions []string
}

func compareVersions(a, b string) int {
	coreA, preA := splitVersion(a)
	coreB, preB := splitVersion(b)

	if c := slices.Compare(coreA[:], coreB[:]); c != 0 {
		return c
	}

	return semver.Compare(prerelease(preA), prerelease(preB))
}

func splitVersion(v string) ([4]int, string) {
	v, _, _ = strings.Cut(v, "+")
	core, pre, _ := strings.Cut(v, "-")

	var parts [4]int
	for i, p := range strings.SplitN(core, ".", 4) {
		parts[i], _ = strconv.Atoi(p)
	}

	return parts, strings.ToLower(pre)
}

func prerelease(label string) string {
	if label == "" {
		return "v0.0.0"
	}

	return "v0.0.0-" + label
}
