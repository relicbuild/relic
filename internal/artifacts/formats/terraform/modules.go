package terraform

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"

	"relic/internal/artifacts/repository"
)

// ErrUnsupportedSource is returned for a module whose upstream registry
// points at a source Relic cannot fetch as an archive.
var ErrUnsupportedSource = errors.New("terraform module source not supported")

// Module is a module's address in a registry: namespace/name/provider.
type Module struct {
	Namespace, Name, Provider string
}

// ParseModule validates the three segments of a module address.
func ParseModule(namespace, name, provider string) (Module, bool) {
	m := Module{
		Namespace: namespace,
		Name:      name,
		Provider:  provider,
	}

	return m, segment.MatchString(namespace) && segment.MatchString(name) && segment.MatchString(provider)
}

// Folder is where a repository stores the module's archives, <version>.zip.
func (m Module) Folder() string { return m.Namespace + "/" + m.Name + "/" + m.Provider }

const (
	maxModuleArchiveBytes  = 256 << 20
	maxModuleFiles         = 20000
	maxModuleUnpackedBytes = 512 << 20
)

// RemoteModuleVersions lists the module's versions in remote repository r's
// upstream registry that the caller may see.
func (s *Service) RemoteModuleVersions(ctx context.Context, r repository.Repository, m Module) ([]string, error) {
	var doc struct {
		Modules []struct {
			Versions []struct {
				Version string `json:"version"`
			} `json:"versions"`
		} `json:"modules"`
	}

	base := strings.TrimSuffix(s.repos.UpstreamOf(r).URL, "/")
	if err := s.upstreamJSON(
		ctx,
		r,
		base+"/v1/modules/"+m.Folder()+"/versions",
		".terraform/modules/"+m.Folder()+"/"+versionsCache,
		&doc,
	); err != nil {
		return nil, err
	}

	var out []string

	for _, list := range doc.Modules {
		for _, v := range list.Versions {
			version := strings.TrimPrefix(v.Version, "v")
			if segment.MatchString(version) && repository.Visible(ctx, r.Name, m.Folder()+"/"+version+".zip") {
				out = append(out, version)
			}
		}
	}

	return out, nil
}

// RemoteModule is version's archive in remote repository r: the cached one,
// or the one its upstream registry points at, fetched, repacked as a zip
// of the module's own directory and cached at <Folder>/<version>.zip.
func (s *Service) RemoteModule(
	ctx context.Context,
	r repository.Repository,
	m Module,
	version string,
) (repository.Item, error) {
	cached := m.Folder() + "/" + version + ".zip"
	if !segment.MatchString(version) {
		return repository.Item{}, ErrNotFound
	}

	if item, err := s.repos.Stat(ctx, r.Name, cached); err == nil {
		return item, nil
	}

	if r.Upstream().Offline {
		return repository.Item{}, ErrNotFound
	}

	return s.moduleFetches.Do(ctx, r.Name+"\x00"+cached, func(ctx context.Context) (repository.Item, error) {
		var item repository.Item

		err := s.repos.WithLock(ctx, "terraform-module:"+r.Name+"\x00"+cached, func() error {
			found, err := s.repos.Stat(ctx, r.Name, cached)
			if err == nil {
				item = found
				return nil
			}

			item, err = s.fetchModule(ctx, r, m, version, cached)

			return err
		})

		return item, err
	})
}

func (s *Service) fetchModule(
	ctx context.Context,
	r repository.Repository,
	m Module,
	version, cached string,
) (repository.Item, error) {
	base := strings.TrimSuffix(s.repos.UpstreamOf(r).URL, "/")
	downloadURL := base + "/v1/modules/" + m.Folder() + "/" + url.PathEscape(version) + "/download"

	location, err := s.moduleLocation(ctx, r, downloadURL)
	if err != nil {
		return repository.Item{}, err
	}

	src, err := parseModuleSource(downloadURL, location)
	if err != nil {
		return repository.Item{}, err
	}

	archive, err := s.fetchModuleArchive(ctx, r, src.url)
	if err != nil {
		return repository.Item{}, err
	}
	defer removeTemporary(archive)

	repacked, err := os.CreateTemp("", "relic-terraform-module-*.zip")
	if err != nil {
		return repository.Item{}, err
	}
	defer removeTemporary(repacked)

	if err := repackModule(archive, src, repacked); err != nil {
		return repository.Item{}, err
	}

	size, err := repacked.Seek(0, io.SeekCurrent)
	if err != nil {
		return repository.Item{}, err
	}

	if _, err := repacked.Seek(0, io.SeekStart); err != nil {
		return repository.Item{}, err
	}

	item, err := s.repos.Cache(ctx, repository.DeployInput{
		Repository: r.Name,
		Path:       cached,
		Body:       repacked,
		Size:       size,
		User:       repository.SystemUser,
	})
	if err != nil {
		return repository.Item{}, fmt.Errorf("cache module %s %s: %w", m.Folder(), version, err)
	}

	return item, nil
}

func (s *Service) moduleLocation(ctx context.Context, r repository.Repository, downloadURL string) (string, error) {
	resp, err := s.upstreamGet(ctx, r, downloadURL)
	if err != nil {
		return "", err
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent:
	case http.StatusNotFound, http.StatusGone:
		return "", ErrNotFound
	default:
		return "", fmt.Errorf("%w: %s returned %s", repository.ErrUpstream, downloadURL, resp.Status)
	}

	if location := resp.Header.Get("X-Terraform-Get"); location != "" {
		return location, nil
	}

	var doc struct {
		Location string `json:"location"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxMetadata)).Decode(&doc); err != nil || doc.Location == "" {
		return "", fmt.Errorf("%w: %s gave no module location", repository.ErrUpstream, downloadURL)
	}

	return doc.Location, nil
}

func (s *Service) upstreamGet(ctx context.Context, r repository.Repository, u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", repository.ErrUpstream, err)
	}

	req.Header.Set("User-Agent", repository.UserAgent)

	if up := s.repos.UpstreamOf(r); up.Username != "" && repository.SameSite(up.URL, u) {
		req.SetBasicAuth(up.Username, up.Password)
	}

	resp, err := s.repos.UpstreamClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", repository.ErrUpstream, err)
	}

	return resp, nil
}

func (s *Service) fetchModuleArchive(ctx context.Context, r repository.Repository, u string) (*os.File, error) {
	resp, err := s.upstreamGet(ctx, r, u)
	if err != nil {
		return nil, err
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound, http.StatusGone:
		return nil, ErrNotFound
	default:
		return nil, fmt.Errorf("%w: module archive returned %s", repository.ErrUpstream, resp.Status)
	}

	archive, err := os.CreateTemp("", "relic-terraform-archive-*")
	if err != nil {
		return nil, err
	}

	n, err := io.Copy(archive, io.LimitReader(resp.Body, maxModuleArchiveBytes+1))
	if err != nil {
		removeTemporary(archive)
		return nil, fmt.Errorf("%w: read module archive: %w", repository.ErrUpstream, err)
	}

	if n > maxModuleArchiveBytes {
		removeTemporary(archive)
		return nil, fmt.Errorf("%w: module archive larger than %d bytes", repository.ErrUpstream, maxModuleArchiveBytes)
	}

	return archive, nil
}

func removeTemporary(f *os.File) {
	_ = f.Close()
	_ = os.Remove(f.Name())
}

type moduleSource struct {
	url      string
	tarGzip  bool
	stripTop bool
	subdir   string
}

var githubRepository = regexp.MustCompile(`^https://github\.com/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+?)(\.git)?$`)

func parseModuleSource(downloadURL, location string) (moduleSource, error) {
	git, isGit := strings.CutPrefix(location, "git::")
	raw := git

	var subdir string

	if scheme, rest, ok := strings.Cut(raw, "://"); ok {
		if i := strings.Index(rest, "//"); i >= 0 {
			after := rest[i+2:]
			rest = rest[:i]

			sub, query, _ := strings.Cut(after, "?")
			subdir = strings.Trim(sub, "/")

			if query != "" {
				rest += "?" + query
			}
		}

		raw = scheme + "://" + rest
	}

	base, err := url.Parse(downloadURL)
	if err != nil {
		return moduleSource{}, fmt.Errorf("%w: %w", ErrUnsupportedSource, err)
	}

	ref, err := url.Parse(raw)
	if err != nil {
		return moduleSource{}, fmt.Errorf("%w: %q", ErrUnsupportedSource, location)
	}

	u := base.ResolveReference(ref)

	if subdir != "" && !localPath(subdir) {
		return moduleSource{}, fmt.Errorf("%w: subdirectory %q", ErrUnsupportedSource, subdir)
	}

	if isGit {
		query := u.Query()
		u.RawQuery = ""

		match := githubRepository.FindStringSubmatch(u.String())
		if match == nil || query.Get("ref") == "" {
			return moduleSource{}, fmt.Errorf("%w: %q", ErrUnsupportedSource, location)
		}

		return moduleSource{
			url: "https://codeload.github.com/" + match[1] + "/" + match[2] + "/zip/" + url.PathEscape(
				query.Get("ref"),
			),
			stripTop: true,
			subdir:   subdir,
		}, nil
	}

	if u.Scheme != "https" && u.Scheme != "http" {
		return moduleSource{}, fmt.Errorf("%w: %q", ErrUnsupportedSource, location)
	}

	query := u.Query()
	kind := query.Get("archive")
	query.Del("archive")
	u.RawQuery = query.Encode()

	switch {
	case kind == "zip" || kind == "" && strings.HasSuffix(u.Path, ".zip"):
		return moduleSource{
			url:    u.String(),
			subdir: subdir,
		}, nil
	case kind == "tar.gz" || kind == "tgz" ||
		kind == "" && (strings.HasSuffix(u.Path, ".tar.gz") || strings.HasSuffix(u.Path, ".tgz")):
		return moduleSource{
			url:     u.String(),
			tarGzip: true,
			subdir:  subdir,
		}, nil
	}

	return moduleSource{}, fmt.Errorf("%w: %q", ErrUnsupportedSource, location)
}

func localPath(p string) bool {
	clean := path.Clean(p)

	return p != "" && clean == p && !path.IsAbs(p) && clean != ".." && !strings.HasPrefix(clean, "../") &&
		!strings.Contains(p, "\\")
}

type moduleWriter struct {
	src     moduleSource
	zw      *zip.Writer
	files   int
	total   int64
	written int
}

func (m *moduleWriter) add(name string, mode int64, r io.Reader) error {
	m.files++
	if m.files > maxModuleFiles {
		return fmt.Errorf("more than %d files", maxModuleFiles)
	}

	body := io.LimitReader(r, maxModuleUnpackedBytes-m.total+1)

	name, keep := m.moduleName(strings.TrimPrefix(name, "./"))
	if !keep {
		n, err := io.Copy(io.Discard, body)
		return m.count(n, err)
	}

	header := &zip.FileHeader{
		Name:   name,
		Method: zip.Deflate,
	}
	header.SetMode(0o644)

	if mode&0o111 != 0 {
		header.SetMode(0o755)
	}

	w, err := m.zw.CreateHeader(header)
	if err != nil {
		return err
	}

	m.written++

	n, err := io.Copy(w, body)

	return m.count(n, err)
}

func (m *moduleWriter) count(n int64, err error) error {
	if err != nil {
		return err
	}

	m.total += n
	if m.total > maxModuleUnpackedBytes {
		return fmt.Errorf("more than %d bytes uncompressed", maxModuleUnpackedBytes)
	}

	return nil
}

func (m *moduleWriter) moduleName(name string) (string, bool) {
	if m.src.stripTop {
		_, name, _ = strings.Cut(name, "/")
	}

	if m.src.subdir != "" {
		var ok bool
		if name, ok = strings.CutPrefix(name, m.src.subdir+"/"); !ok {
			return "", false
		}
	}

	return name, name != "" && localPath(name)
}

func repackModule(archive *os.File, src moduleSource, out io.Writer) error {
	m := &moduleWriter{
		src: src,
		zw:  zip.NewWriter(out),
	}

	if err := m.addArchive(archive); err != nil {
		return fmt.Errorf("%w: module archive: %w", repository.ErrUpstream, err)
	}

	if err := m.zw.Close(); err != nil {
		return err
	}

	if m.written == 0 {
		return fmt.Errorf("%w: the module archive has no files under %q", repository.ErrUpstream, src.subdir)
	}

	return nil
}

func (m *moduleWriter) addArchive(archive *os.File) error {
	if !m.src.tarGzip {
		info, err := archive.Stat()
		if err != nil {
			return err
		}

		zr, err := zip.NewReader(archive, info.Size())
		if err != nil {
			return err
		}

		for _, f := range zr.File {
			if !f.Mode().IsRegular() {
				continue
			}

			rc, err := f.Open()
			if err != nil {
				return err
			}

			err = m.add(f.Name, int64(f.Mode().Perm()), rc)
			_ = rc.Close()

			if err != nil {
				return err
			}
		}

		return nil
	}

	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return err
	}

	gz, err := gzip.NewReader(archive)
	if err != nil {
		return err
	}

	tr := tar.NewReader(gz)

	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}

		if err != nil {
			return err
		}

		if header.Typeflag != tar.TypeReg {
			continue
		}

		if err := m.add(header.Name, header.Mode, tr); err != nil {
			return err
		}
	}
}
