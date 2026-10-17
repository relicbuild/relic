// Package maven implements Maven repository semantics on Relic
// repositories: server-calculated maven-metadata.xml for artifacts and
// unique SNAPSHOT versions, and metadata merging for virtual repositories.
package maven

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"relic/internal/artifacts/repository"
)

// Format is the repository format this package serves.
const Format = repository.FormatMaven

// MetadataFile is the name of Maven metadata files.
const MetadataFile = "maven-metadata.xml"

// SystemUser records server-calculated files.
const SystemUser = repository.SystemUser

// Service implements Maven metadata on top of repo.Service.
type Service struct {
	repos *repository.Service
	log   *slog.Logger
	now   func() time.Time
}

// NewService returns a Service and registers its deploy hook for maven and
// gradle repositories.
func NewService(repos *repository.Service, log *slog.Logger) *Service {
	s := &Service{
		repos: repos,
		log:   log,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
	repos.RegisterMetadata(IsMetadata)

	for _, f := range []repository.Format{
		Format,
		"gradle",
	} {
		repos.RegisterHook(f, s.afterDeploy)
		repos.RegisterDeleteHook(f, s.afterDelete)
		repos.SetUpstreamCheck(f, s.publishedChecksums)
	}

	return s
}

func (s *Service) recalculate(ctx context.Context, repoKey string, dirs ...string) error {
	for _, dir := range dirs {
		if err := s.RecalculateDir(ctx, repoKey, dir); err != nil {
			return fmt.Errorf("recalculate %s/%s: %w", repoKey, dir, err)
		}
	}

	return nil
}

// RecalculateDir rewrites the maven-metadata.xml of an artifact directory, or
// of a SNAPSHOT version directory, from the files it holds. Recalculations
// of one directory run one at a time, so the last one sees every change.
func (s *Service) RecalculateDir(ctx context.Context, repoKey, dir string) error {
	return s.repos.WithLock(ctx, "maven-metadata:"+repoKey+"/"+dir, func() error {
		return s.recalculateDir(ctx, repoKey, dir)
	})
}

func (s *Service) recalculateDir(ctx context.Context, repoKey, dir string) error {
	segs := strings.Split(dir, "/")

	var err error

	n := len(segs)
	switch {
	case n >= 3 && IsSnapshot(segs[n-1]):
		err = s.writeSnapshotMetadata(ctx, repoKey, strings.Join(segs[:n-2], "/"), segs[n-2], segs[n-1])
	case n >= 2:
		err = s.writeArtifactMetadata(ctx, repoKey, strings.Join(segs[:n-1], "/"), segs[n-1])
	}

	if errors.Is(err, repository.ErrNotFound) {
		return nil
	}

	return err
}

// afterDelete recalculates the metadata of the directory that held a deleted
// item and, below a SNAPSHOT version, of its artifact directory. Group
// metadata (plugins) is left alone.
func (s *Service) afterDelete(ctx context.Context, r repository.Repository, item repository.Item) error {
	if r.Class != "local" {
		return nil
	}

	parent := path.Dir(item.Path())

	dirs := []string{
		parent,
	}
	if IsSnapshot(path.Base(parent)) {
		dirs = append(dirs, path.Dir(parent))
	}

	var stale []string

	for _, dir := range dirs {
		md, err := s.storedMetadata(ctx, r.Name, dir)
		if err == nil && len(md.Plugins) == 0 {
			stale = append(stale, dir)
		}
	}

	return s.recalculate(ctx, r.Name, stale...)
}

func (s *Service) storedMetadata(ctx context.Context, repoKey, dir string) (Metadata, error) {
	item, err := s.repos.Stat(ctx, repoKey, dir+"/"+MetadataFile)
	if err != nil {
		return Metadata{}, err
	}

	body, err := s.repos.Open(ctx, item, nil)
	if err != nil {
		return Metadata{}, err
	}

	defer func() {
		_ = body.Close()
	}()

	return Parse(body)
}

// Metadata is maven-metadata.xml.
type Metadata struct {
	XMLName      xml.Name    `xml:"metadata"`
	ModelVersion string      `xml:"modelVersion,attr,omitempty"`
	GroupID      string      `xml:"groupId,omitempty"`
	ArtifactID   string      `xml:"artifactId,omitempty"`
	Version      string      `xml:"version,omitempty"`
	Versioning   *Versioning `xml:"versioning,omitempty"`
	Plugins      []Plugin    `xml:"plugins>plugin,omitempty"`
}

// Plugin is a <plugin> entry of group-level metadata.
type Plugin struct {
	Name       string `xml:"name,omitempty"`
	Prefix     string `xml:"prefix"`
	ArtifactID string `xml:"artifactId"`
}

// Versioning is the <versioning> element.
type Versioning struct {
	Latest           string            `xml:"latest,omitempty"`
	Release          string            `xml:"release,omitempty"`
	Snapshot         *Snapshot         `xml:"snapshot,omitempty"`
	Versions         []string          `xml:"versions>version,omitempty"`
	LastUpdated      string            `xml:"lastUpdated,omitempty"`
	SnapshotVersions []SnapshotVersion `xml:"snapshotVersions>snapshotVersion,omitempty"`
}

// Snapshot is the newest unique snapshot build.
type Snapshot struct {
	Timestamp   string `xml:"timestamp,omitempty"`
	BuildNumber int    `xml:"buildNumber,omitempty"`
}

// SnapshotVersion maps a classifier/extension to its newest unique file.
type SnapshotVersion struct {
	Classifier string `xml:"classifier,omitempty"`
	Extension  string `xml:"extension"`
	Value      string `xml:"value"`
	Updated    string `xml:"updated"`
}

// Coordinates locate a file in the Maven layout:
// <group path>/<artifactId>/<version>/<file>.
type Coordinates struct {
	GroupPath, ArtifactID, Version, File string
}

// ParsePath returns the coordinates of a file in the Maven layout.
func ParsePath(p string) (Coordinates, bool) {
	segs := strings.Split(p, "/")
	if len(segs) < 4 {
		return Coordinates{}, false
	}

	n := len(segs)

	return Coordinates{
		GroupPath:  strings.Join(segs[:n-3], "/"),
		ArtifactID: segs[n-3],
		Version:    segs[n-2],
		File:       segs[n-1],
	}, true
}

var checksumSuffixes = []string{
	".sha1",
	".md5",
	".sha256",
	".sha512",
	".asc",
}

func isChecksum(name string) bool {
	return slices.ContainsFunc(checksumSuffixes, func(s string) bool {
		return strings.HasSuffix(name, s)
	})
}

func (s *Service) afterDeploy(ctx context.Context, r repository.Repository, item repository.Item) error {
	if r.Class != "local" {
		return nil
	}

	if (item.ModifiedBy == SystemUser && item.Name == MetadataFile) || isChecksum(item.Name) {
		return nil
	}

	if item.Name == MetadataFile {
		if md, err := s.storedMetadata(ctx, r.Name, item.Parent); err == nil && len(md.Plugins) > 0 {
			return nil
		}

		segs := strings.Split(item.Parent, "/")
		if n := len(segs); n >= 3 && IsSnapshot(segs[n-1]) {
			return s.recalculate(ctx, r.Name, item.Parent, path.Dir(item.Parent))
		} else if n >= 2 {
			return s.recalculate(ctx, r.Name, item.Parent)
		}

		return nil
	}

	c, ok := ParsePath(item.Path())
	if !ok {
		return nil
	}

	artifactDir := c.GroupPath + "/" + c.ArtifactID
	if IsSnapshot(c.Version) {
		return s.recalculate(ctx, r.Name, artifactDir+"/"+c.Version, artifactDir)
	}

	return s.recalculate(ctx, r.Name, artifactDir)
}

func (s *Service) children(ctx context.Context, repoKey, folder string) ([]repository.Item, error) {
	f, err := s.repos.Stat(ctx, repoKey, folder)
	if err != nil {
		return nil, err
	}

	return s.repos.Children(ctx, f)
}

func (s *Service) writeArtifactMetadata(ctx context.Context, repoKey, groupPath, artifactID string) error {
	dir := groupPath + "/" + artifactID

	entries, err := s.children(ctx, repoKey, dir)
	if err != nil {
		return err
	}

	var versions []string

	for _, e := range entries {
		if e.IsFolder() {
			versions = append(versions, e.Name)
		}
	}

	if len(versions) == 0 {
		err := s.repos.Delete(ctx, repoKey, dir+"/"+MetadataFile)
		if errors.Is(err, repository.ErrNotFound) {
			return nil
		}

		return err
	}

	sort.Slice(versions, func(i, j int) bool {
		return Compare(versions[i], versions[j]) < 0
	})

	v := &Versioning{
		Latest:      versions[len(versions)-1],
		Versions:    versions,
		LastUpdated: s.now().Format("20060102150405"),
	}
	for _, version := range slices.Backward(versions) {
		if !IsSnapshot(version) {
			v.Release = version
			break
		}
	}

	return s.write(
		ctx,
		repoKey,
		dir,
		Metadata{
			GroupID:    strings.ReplaceAll(groupPath, "/", "."),
			ArtifactID: artifactID,
			Versioning: v,
		},
	)
}

func uniqueSnapshot(artifactID, base string) *regexp.Regexp {
	return regexp.MustCompile(`^` + regexp.QuoteMeta(artifactID+"-"+base) + `-(\d{8}\.\d{6})-(\d+)(?:-([^.]+))?\.(.+)$`)
}

func (s *Service) writeSnapshotMetadata(ctx context.Context, repoKey, groupPath, artifactID, version string) error {
	dir := groupPath + "/" + artifactID + "/" + version

	entries, err := s.children(ctx, repoKey, dir)
	if err != nil {
		return err
	}

	re := uniqueSnapshot(artifactID, strings.TrimSuffix(version, "-SNAPSHOT"))

	type newest struct {
		ts    string
		build int
		value string
		upd   time.Time
	}

	latest := map[string]newest{}

	var top newest

	for _, e := range entries {
		if e.IsFolder() || isChecksum(e.Name) || e.Name == MetadataFile {
			continue
		}

		m := re.FindStringSubmatch(e.Name)
		if m == nil {
			continue
		}

		var build int

		_, _ = fmt.Sscanf(m[2], "%d", &build)
		n := newest{
			ts:    m[1],
			build: build,
			value: strings.TrimSuffix(version, "-SNAPSHOT") + "-" + m[1] + "-" + m[2],
			upd:   e.Modified,
		}

		key := m[3] + "|" + m[4]
		if cur, ok := latest[key]; !ok || n.build > cur.build || (n.build == cur.build && n.ts > cur.ts) {
			latest[key] = n
		}

		if n.build > top.build || (n.build == top.build && n.ts > top.ts) {
			top = n
		}
	}

	md := Metadata{
		ModelVersion: "1.1.0",
		GroupID:      strings.ReplaceAll(groupPath, "/", "."),
		ArtifactID:   artifactID,
		Version:      version,
	}

	v := &Versioning{
		LastUpdated: s.now().Format("20060102150405"),
	}
	if top.ts != "" {
		v.Snapshot = &Snapshot{
			Timestamp:   top.ts,
			BuildNumber: top.build,
		}

		keys := make([]string, 0, len(latest))
		for k := range latest {
			keys = append(keys, k)
		}

		sort.Strings(keys)

		for _, k := range keys {
			classifier, ext, _ := strings.Cut(k, "|")
			n := latest[k]
			v.SnapshotVersions = append(
				v.SnapshotVersions,
				SnapshotVersion{
					Classifier: classifier,
					Extension:  ext,
					Value:      n.value,
					Updated:    n.upd.UTC().Format("20060102150405"),
				},
			)
		}
	}

	md.Versioning = v

	return s.write(ctx, repoKey, dir, md)
}

func (s *Service) write(ctx context.Context, repoKey, dir string, md Metadata) error {
	body, err := Marshal(md)
	if err != nil {
		return err
	}

	_, err = s.repos.Deploy(
		ctx,
		repository.DeployInput{
			Repository: repoKey,
			Path:       dir + "/" + MetadataFile,
			Body:       bytes.NewReader(body),
			User:       SystemUser,
		},
	)

	return err
}

// Marshal renders metadata with an XML declaration.
func Marshal(md Metadata) ([]byte, error) {
	out, err := xml.MarshalIndent(md, "", "  ")
	if err != nil {
		return nil, err
	}

	return append([]byte(xml.Header), append(out, '\n')...), nil
}

// Merge combines metadata documents from a virtual repository's members:
// the union of versions, the highest latest/release, the newest snapshot,
// and the union of group plugins (the first member's wins for a prefix).
func Merge(docs []Metadata) Metadata {
	if len(docs) == 0 {
		return Metadata{}
	}

	out := Metadata{
		ModelVersion: docs[0].ModelVersion,
		GroupID:      docs[0].GroupID,
		ArtifactID:   docs[0].ArtifactID,
		Version:      docs[0].Version,
	}
	prefixes := map[string]bool{}

	for _, d := range docs {
		for _, p := range d.Plugins {
			if !prefixes[p.Prefix] {
				prefixes[p.Prefix] = true
				out.Plugins = append(out.Plugins, p)
			}
		}
	}

	v := &Versioning{}
	versioned := false
	seen := map[string]bool{}

	for _, d := range docs {
		if d.Versioning == nil {
			continue
		}

		versioned = true

		dv := d.Versioning
		for _, ver := range dv.Versions {
			if !seen[ver] {
				seen[ver] = true
				v.Versions = append(v.Versions, ver)
			}
		}

		if dv.Latest != "" && (v.Latest == "" || Compare(dv.Latest, v.Latest) > 0) {
			v.Latest = dv.Latest
		}

		if dv.Release != "" && (v.Release == "" || Compare(dv.Release, v.Release) > 0) {
			v.Release = dv.Release
		}

		if dv.LastUpdated > v.LastUpdated {
			v.LastUpdated = dv.LastUpdated
		}

		if dv.Snapshot != nil && (v.Snapshot == nil || dv.Snapshot.Timestamp > v.Snapshot.Timestamp) {
			v.Snapshot = dv.Snapshot
		}

		v.SnapshotVersions = mergeSnapshotVersions(v.SnapshotVersions, dv.SnapshotVersions)
	}

	sort.Slice(v.Versions, func(i, j int) bool {
		return Compare(v.Versions[i], v.Versions[j]) < 0
	})

	if versioned {
		out.Versioning = v
	}

	return out
}

func mergeSnapshotVersions(merged, more []SnapshotVersion) []SnapshotVersion {
	for _, candidate := range more {
		i := slices.IndexFunc(merged, func(sv SnapshotVersion) bool {
			return sv.Classifier == candidate.Classifier && sv.Extension == candidate.Extension
		})

		switch {
		case i < 0:
			merged = append(merged, candidate)
		case candidate.Updated > merged[i].Updated:
			merged[i] = candidate
		}
	}

	return merged
}

// Parse decodes maven-metadata.xml.
func Parse(r io.Reader) (Metadata, error) {
	var md Metadata
	if err := xml.NewDecoder(io.LimitReader(r, 16<<20)).Decode(&md); err != nil {
		return Metadata{}, fmt.Errorf("parse %s: %w", MetadataFile, err)
	}

	return md, nil
}

// ServesMergedMetadata reports whether path is maven-metadata.xml in a
// virtual Maven (or Gradle) repository, which is served merged.
func (s *Service) ServesMergedMetadata(ctx context.Context, repoKey, path string) bool {
	if !strings.HasSuffix(path, "/"+MetadataFile) && path != MetadataFile {
		return false
	}

	r, err := s.repos.GetRepository(ctx, repoKey)

	return err == nil && r.Class == "virtual" && isMavenType(r.Format)
}

func isMavenType(t repository.Format) bool {
	return t == Format || t == repository.FormatGradle || t == repository.FormatIvy || t == repository.FormatSbt
}

// MergedMetadata merges path's metadata across a virtual repository's
// members (resolving remotes through their caches).
func (s *Service) MergedMetadata(ctx context.Context, virtualKey, path string) ([]byte, error) {
	r, err := s.repos.GetRepository(ctx, virtualKey)
	if err != nil {
		return nil, err
	}

	var docs []Metadata

	for _, member := range s.repos.Flatten(ctx, r, path) {
		item, err := s.repos.Resolve(ctx, member.Name, path)
		if err != nil {
			continue
		}

		rc, err := s.repos.Open(ctx, item, nil)
		if err != nil {
			return nil, err
		}

		md, err := Parse(rc)
		_ = rc.Close()

		if err != nil {
			s.log.WarnContext(ctx, "skipping unreadable metadata", "repo", member.Name, "path", path, "error", err)
			continue
		}

		docs = append(docs, md)
	}

	if len(docs) == 0 {
		return nil, repository.ErrNotFound
	}

	return Marshal(Merge(docs))
}

// IsMetadata reports whether a file name is Maven metadata, which remote
// repositories re-fetch after their retrieval cache period.
func IsMetadata(name string) bool { return name == MetadataFile }
