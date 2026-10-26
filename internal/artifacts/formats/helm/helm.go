// Package helm implements classic Helm chart repositories on Relic
// repositories.
package helm

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

	"relic/internal/artifacts/formats/indexcache"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/telemetry"

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

var indexSettings = indexcache.Settings{
	Fresh:   time.Hour,
	Stale:   24 * time.Hour,
	Entries: 64,
	Bytes:   256 << 20,
	Builds:  2,
	Timeout: 10 * time.Minute,
}

// Service implements chart repositories on top of repo.Service.
type Service struct {
	repos   *repository.Service
	log     *slog.Logger
	now     func() time.Time
	indexes *indexcache.Cache[[]byte]

	mu          sync.Mutex
	metas       map[string]map[string]any
	foreignURLs map[string]map[string]string
}

// NewService returns a Service and registers its deploy hook and the
// index as remote metadata.
func NewService(repos *repository.Service, log *slog.Logger) *Service {
	s := &Service{
		repos:       repos,
		log:         log,
		now:         time.Now,
		metas:       map[string]map[string]any{},
		foreignURLs: map[string]map[string]string{},
	}
	s.indexes = indexcache.New[[]byte](string(Format), indexSettings, func(body []byte) int64 {
		return int64(len(body))
	}, func() time.Time {
		return s.now()
	})
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

// BuildIndex returns r's index.yaml; callers whose visibility hides nothing
// in r's members share one build per content of the members.
func (s *Service) BuildIndex(ctx context.Context, r repository.Repository) ([]byte, error) {
	members := s.members(ctx, r, 0)
	if repository.AnyRestricted(ctx, members) {
		sources, err := s.sources(ctx, members)
		if err != nil {
			return nil, err
		}

		return s.buildIndex(ctx, r, sources)
	}

	ctx = repository.Unfiltered(ctx)

	sources, err := s.sources(ctx, members)
	if err != nil {
		return nil, err
	}

	return s.indexes.Get(ctx, indexKey(sources), func(ctx context.Context) ([]byte, error) {
		return s.buildIndex(ctx, r, sources)
	})
}

type memberIndex struct {
	repo     repository.Repository
	upstream *repository.Item
	charts   []repository.Item
	err      error
}

func (s *Service) sources(ctx context.Context, members []repository.Repository) ([]memberIndex, error) {
	out := make([]memberIndex, 0, len(members))

	for _, m := range members {
		source := memberIndex{
			repo: m,
		}

		if m.Class == repository.ClassRemote {
			item, err := s.repos.Resolve(ctx, m.Name, IndexFile)

			switch {
			case err == nil:
				source.upstream = &item
			case errors.Is(err, repository.ErrUpstream):
				source.err = err
			case !errors.Is(err, repository.ErrNotFound):
				return nil, err
			}
		} else {
			charts, err := s.localCharts(ctx, m.Name)
			if err != nil {
				return nil, err
			}

			source.charts = charts
		}

		out = append(out, source)
	}

	return out, nil
}

func indexKey(sources []memberIndex) string {
	digest := sha256.New()

	for _, source := range sources {
		_, _ = fmt.Fprintf(digest, "%s\x00%s\x00", source.repo.Name, source.repo.Upstream().URL)

		switch {
		case source.err != nil:
			_, _ = io.WriteString(digest, "unavailable\x00")
		case source.upstream != nil:
			_, _ = fmt.Fprintf(digest, "upstream\x00%s\x00", source.upstream.Checksums.SHA256)
		}

		for _, chart := range source.charts {
			_, _ = fmt.Fprintf(
				digest,
				"%s\x00%s\x00%d\x00",
				chart.Path(),
				chart.Checksums.SHA256,
				chart.Created.UnixNano(),
			)
		}

		_, _ = io.WriteString(digest, "\x01")
	}

	return hex.EncodeToString(digest.Sum(nil))
}

func (s *Service) buildIndex(
	ctx context.Context,
	r repository.Repository,
	sources []memberIndex,
) (body []byte, err error) {
	ctx, span := telemetry.Tracer().Start(ctx, "helm.BuildIndex")
	started := time.Now()

	defer func() {
		telemetry.End(span, err)
		telemetry.IndexBuilt(ctx, string(Format), string(r.Class), err, time.Since(started))
	}()

	idx := Index{
		APIVersion: "v1",
		Entries:    map[string][]map[string]any{},
		Generated:  s.now().UTC().Format(time.RFC3339),
	}
	seen := map[string]bool{}

	var upstreamErr error

	for _, source := range sources {
		var entries []map[string]any

		switch {
		case source.err != nil:
			err = source.err
		case source.upstream != nil:
			entries, _, err = s.remoteEntries(ctx, source.repo, *source.upstream)
		default:
			entries = s.localEntries(ctx, source.repo.Name, source.charts)
		}

		if errors.Is(err, repository.ErrUpstream) {
			s.log.WarnContext(ctx, "skipping member index", "repo", r.Name, "member", source.repo.Name, "error", err)

			upstreamErr, err = err, nil

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

func (s *Service) localCharts(ctx context.Context, repoKey string) ([]repository.Item, error) {
	root, err := s.repos.Stat(ctx, repoKey, "")
	if err != nil {
		return nil, err
	}

	items, err := s.repos.Subtree(ctx, root)
	if err != nil {
		return nil, err
	}

	return slices.DeleteFunc(items, func(it repository.Item) bool {
		return it.IsFolder() || !strings.HasSuffix(it.Name, ".tgz")
	}), nil
}

func (s *Service) localEntries(ctx context.Context, repoKey string, charts []repository.Item) []map[string]any {
	entries := make([]map[string]any, 0, len(charts))

	for _, it := range charts {
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

	return entries
}

func (s *Service) remoteEntries(
	ctx context.Context,
	r repository.Repository,
	item repository.Item,
) ([]map[string]any, map[string]string, error) {
	rc, err := s.repos.Open(ctx, item, nil)
	if err != nil {
		return nil, nil, err
	}

	defer func() {
		_ = rc.Close()
	}()

	var idx Index
	if err := yaml.NewDecoder(io.LimitReader(rc, maxIndexBytes)).Decode(&idx); err != nil && !errors.Is(err, io.EOF) {
		return nil, nil, fmt.Errorf("%w: index of %s: %w", repository.ErrUpstream, r.Name, err)
	}

	base, err := url.Parse(strings.TrimSuffix(s.repos.UpstreamOf(r).URL, "/") + "/")
	if err != nil {
		return nil, nil, fmt.Errorf("%w: url of %s: %w", repository.ErrUpstream, r.Name, err)
	}

	var entries []map[string]any

	restricted := repository.Restricted(ctx, r.Name)
	foreign := map[string]string{}

	for _, list := range idx.Entries {
		for _, e := range list {
			urls := relativeURLs(e["urls"], base, foreign)
			if restricted && len(urls) > 0 && !strings.Contains(urls[0], "://") &&
				!repository.Visible(ctx, r.Name, unescaped(urls[0])) {
				continue
			}

			e["urls"] = urls
			entries = append(entries, e)
		}
	}

	s.mu.Lock()
	if len(s.foreignURLs) >= foreignIndexLimit {
		clear(s.foreignURLs)
	}

	s.foreignURLs[item.Checksums.SHA256] = foreign
	s.mu.Unlock()

	return entries, foreign, nil
}

// ForeignPrefix starts the paths a remote's index gives the charts its
// upstream index lists on another host: ForeignPrefix<host>/<path>.
const ForeignPrefix = "_upstream/"

const foreignIndexLimit = 256

func relativeURLs(v any, base *url.URL, foreign map[string]string) []string {
	list, _ := v.([]any)

	var out []string

	for _, u := range list {
		raw, _ := u.(string)

		ref, err := url.Parse(raw)
		if err != nil {
			continue
		}

		abs := base.ResolveReference(ref)
		rel, onSite := strings.CutPrefix(abs.String(), base.String())

		switch {
		case onSite && abs.Host == base.Host:
			out = append(out, rel)
		case (abs.Scheme == "https" || abs.Scheme == "http") && abs.Host != "" && abs.User == nil:
			p := ForeignPrefix + abs.Host + "/" + strings.TrimPrefix(abs.Path, "/")
			foreign[p] = abs.String()
			out = append(out, escapePath(p))
		default:
			out = append(out, abs.String())
		}
	}

	return out
}

// ForeignChart fetches through r's remote members, and caches at p, the chart a remote's upstream index lists on
// another host and Relic serves at p, for URLs that index lists only.
func (s *Service) ForeignChart(ctx context.Context, r repository.Repository, p string) (repository.Item, error) {
	if !strings.HasPrefix(p, ForeignPrefix) {
		return repository.Item{}, repository.ErrNotFound
	}

	for _, m := range s.members(ctx, r, 0) {
		if m.Class != "remote" {
			continue
		}

		upstreamURL, err := s.foreignURL(ctx, m, p)
		if err != nil {
			return repository.Item{}, err
		}

		if upstreamURL == "" {
			continue
		}

		return s.repos.ResolveURL(ctx, m, upstreamURL, p)
	}

	return repository.Item{}, repository.ErrNotFound
}

func (s *Service) foreignURL(ctx context.Context, r repository.Repository, p string) (string, error) {
	item, err := s.repos.Resolve(ctx, r.Name, IndexFile)
	if errors.Is(err, repository.ErrNotFound) {
		return "", nil
	}

	if err != nil {
		return "", err
	}

	s.mu.Lock()
	foreign, ok := s.foreignURLs[item.Checksums.SHA256]
	s.mu.Unlock()

	if !ok {
		if _, foreign, err = s.remoteEntries(ctx, r, item); err != nil {
			return "", err
		}
	}

	return foreign[p], nil
}

func compareVersions(a, b string) int {
	va, vb := "v"+strings.TrimPrefix(a, "v"), "v"+strings.TrimPrefix(b, "v")
	if semver.IsValid(va) && semver.IsValid(vb) {
		return semver.Compare(va, vb)
	}

	return strings.Compare(a, b)
}

func str(v any) string { s, _ := v.(string); return s }

func unescaped(p string) string {
	if u, err := url.PathUnescape(p); err == nil {
		return u
	}

	return p
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, seg := range parts {
		parts[i] = url.PathEscape(seg)
	}

	return strings.Join(parts, "/")
}
