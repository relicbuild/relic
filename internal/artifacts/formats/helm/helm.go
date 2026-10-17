// Package helm implements classic Helm chart repositories on Relic
// repositories.
package helm

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/url"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"relic/internal/artifacts/repository"

	"go.yaml.in/yaml/v3"
	"golang.org/x/mod/semver"
)

// Format is the repository format this package serves.
const Format = repository.FormatHelm

// IndexFile is the chart repository index.
const IndexFile = "index.yaml"

const (
	maxChartYAML   = 1 << 20
	maxIndexBytes  = 64 << 20
	metaCacheLimit = 20000
)

// ErrInvalidChart is returned for a .tgz without a readable Chart.yaml.
var ErrInvalidChart = errors.New("not a Helm chart")

// Service implements chart repositories on top of repo.Service.
type Service struct {
	repos *repository.Service
	log   *slog.Logger

	mu    sync.Mutex
	metas map[string]map[string]any
}

// NewService returns a Service and registers its deploy hook and the
// index as remote metadata.
func NewService(repos *repository.Service, log *slog.Logger) *Service {
	s := &Service{
		repos: repos,
		log:   log,
		metas: map[string]map[string]any{},
	}
	repos.RegisterHook(Format, s.indexChart)
	repos.RegisterMetadata(func(name string) bool {
		return name == IndexFile
	})

	return s
}

func (s *Service) indexChart(ctx context.Context, r repository.Repository, item repository.Item) error {
	if !strings.HasSuffix(item.Name, ".tgz") || r.Class != "local" {
		return nil
	}

	meta, err := s.chartMeta(ctx, item)
	if err != nil {
		return err
	}

	props := repository.Properties{}

	for key, prop := range map[string]string{
		"name":        "chart.name",
		"version":     "chart.version",
		"description": "chart.description",
		"appVersion":  "appVersion",
	} {
		if v, ok := meta[key].(string); ok && v != "" {
			props[prop] = []string{
				v,
			}
		}
	}

	return s.repos.SetProperties(ctx, item, props, false)
}

func (s *Service) chartMeta(ctx context.Context, item repository.Item) (map[string]any, error) {
	s.mu.Lock()
	meta, ok := s.metas[item.Checksums.SHA256]
	s.mu.Unlock()

	if ok {
		return meta, nil
	}

	rc, err := s.repos.Open(ctx, item, nil)
	if err != nil {
		return nil, err
	}

	defer func() {
		_ = rc.Close()
	}()

	meta, err = readChartYAML(rc)
	if err != nil {
		return nil, fmt.Errorf("%w: %s/%s: %w", ErrInvalidChart, item.Repository, item.Path(), err)
	}

	s.mu.Lock()
	if len(s.metas) >= metaCacheLimit {
		clear(s.metas)
	}

	s.metas[item.Checksums.SHA256] = meta
	s.mu.Unlock()

	return meta, nil
}

func readChartYAML(r io.Reader) (map[string]any, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("no chart metadata file")
		}

		if err != nil {
			return nil, err
		}

		dir, file := path.Split(hdr.Name)
		if file != "Chart.yaml" || strings.Count(strings.Trim(dir, "/"), "/") != 0 || dir == "" {
			continue
		}

		body, err := io.ReadAll(io.LimitReader(tr, maxChartYAML))
		if err != nil {
			return nil, err
		}

		var meta map[string]any
		if err := yaml.Unmarshal(body, &meta); err != nil {
			return nil, err
		}

		for _, k := range []string{
			"version",
			"appVersion",
		} {
			if v, ok := meta[k]; ok && v != nil {
				meta[k] = fmt.Sprint(v)
			}
		}

		if n, _ := meta["name"].(string); n == "" {
			return nil, errors.New("chart metadata has no name")
		}

		if v, _ := meta["version"].(string); v == "" {
			return nil, errors.New("chart metadata has no version")
		}

		return meta, nil
	}
}

// Index is a chart repository index.
type Index struct {
	APIVersion string                      `yaml:"apiVersion"`
	Entries    map[string][]map[string]any `yaml:"entries"`
	Generated  string                      `yaml:"generated"`
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

// BuildIndex returns r's index.yaml.
func (s *Service) BuildIndex(ctx context.Context, r repository.Repository) ([]byte, error) {
	idx := Index{
		APIVersion: "v1",
		Entries:    map[string][]map[string]any{},
		Generated:  time.Now().UTC().Format(time.RFC3339),
	}
	seen := map[string]bool{}

	var upstreamErr error

	for _, m := range s.members(ctx, r, 0) {
		var (
			entries []map[string]any
			err     error
		)
		if m.Class == "remote" {
			entries, err = s.remoteEntries(ctx, m)
		} else {
			entries, err = s.localEntries(ctx, m.Name)
		}

		if errors.Is(err, repository.ErrUpstream) {
			s.log.WarnContext(ctx, "skipping member index", "repo", r.Name, "member", m.Name, "error", err)

			upstreamErr = err

			continue
		}

		if err != nil {
			return nil, err
		}

		for _, e := range entries {
			name, _ := e["name"].(string)

			version, _ := e["version"].(string)
			if name == "" || seen[name+"\x00"+version] {
				continue
			}

			seen[name+"\x00"+version] = true
			idx.Entries[name] = append(idx.Entries[name], e)
		}
	}

	if len(idx.Entries) == 0 && upstreamErr != nil {
		return nil, upstreamErr
	}

	for _, list := range idx.Entries {
		slices.SortStableFunc(
			list,
			func(a, b map[string]any) int {
				return -compareVersions(str(a["version"]), str(b["version"]))
			},
		)
	}

	return yaml.Marshal(idx)
}

func (s *Service) localEntries(ctx context.Context, repoKey string) ([]map[string]any, error) {
	root, err := s.repos.Stat(ctx, repoKey, "")
	if err != nil {
		return nil, err
	}

	items, err := s.repos.Subtree(ctx, root)
	if err != nil {
		return nil, err
	}

	var entries []map[string]any

	for _, it := range items {
		if it.IsFolder() || !strings.HasSuffix(it.Name, ".tgz") {
			continue
		}

		meta, err := s.chartMeta(ctx, it)
		if err != nil {
			s.log.WarnContext(ctx, "skipping chart in index", "repo", repoKey, "path", it.Path(), "error", err)
			continue
		}

		e := make(map[string]any, len(meta)+3)
		maps.Copy(e, meta)
		e["created"] = it.Created.UTC().Format(time.RFC3339Nano)
		e["digest"] = it.Checksums.SHA256
		e["urls"] = []string{
			escapePath(it.Path()),
		}
		entries = append(entries, e)
	}

	return entries, nil
}

func (s *Service) remoteEntries(ctx context.Context, r repository.Repository) ([]map[string]any, error) {
	item, err := s.repos.Resolve(ctx, r.Name, IndexFile)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, nil
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

	body, err := io.ReadAll(io.LimitReader(rc, maxIndexBytes))
	if err != nil {
		return nil, err
	}

	var idx Index
	if err := yaml.Unmarshal(body, &idx); err != nil {
		return nil, fmt.Errorf("%w: index of %s: %w", repository.ErrUpstream, r.Name, err)
	}

	base, err := url.Parse(strings.TrimSuffix(s.repos.UpstreamOf(r).URL, "/") + "/")
	if err != nil {
		return nil, fmt.Errorf("%w: url of %s: %w", repository.ErrUpstream, r.Name, err)
	}

	var entries []map[string]any

	restricted := repository.Restricted(ctx, r.Name)

	for _, list := range idx.Entries {
		for _, e := range list {
			urls := relativeURLs(e["urls"], base)
			if restricted && len(urls) > 0 && !strings.Contains(urls[0], "://") &&
				!repository.Visible(ctx, r.Name, urls[0]) {
				continue
			}

			e["urls"] = urls
			entries = append(entries, e)
		}
	}

	return entries, nil
}

func relativeURLs(v any, base *url.URL) []string {
	list, _ := v.([]any)

	var out []string

	for _, u := range list {
		raw, _ := u.(string)

		ref, err := url.Parse(raw)
		if err != nil {
			continue
		}

		abs := base.ResolveReference(ref)
		if rel, ok := strings.CutPrefix(abs.String(), base.String()); ok && abs.Host == base.Host {
			out = append(out, rel)
		} else {
			out = append(out, abs.String())
		}
	}

	return out
}

func compareVersions(a, b string) int {
	va, vb := "v"+strings.TrimPrefix(a, "v"), "v"+strings.TrimPrefix(b, "v")
	if semver.IsValid(va) && semver.IsValid(vb) {
		return semver.Compare(va, vb)
	}

	return strings.Compare(a, b)
}

func str(v any) string { s, _ := v.(string); return s }

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, seg := range parts {
		parts[i] = url.PathEscape(seg)
	}

	return strings.Join(parts, "/")
}
