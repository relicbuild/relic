// Package npm implements the npm registry protocol on Relic
// repositories: packuments, tarballs, publish, dist-tags and search, for
// local, remote and virtual repositories.
package npm

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"relic/internal/artifacts/repository"
	"relic/internal/platform/database"
)

// Format is the repository format this package serves.
const Format = repository.FormatNpm

// Errors mapped by the API layer.
var (
	ErrNotFound      = errors.New("package not found")
	ErrInvalid       = errors.New("invalid npm request")
	ErrVersionExists = errors.New("cannot publish over an existing version")
)

const maxManifestBytes = 4 << 20

var validName = regexp.MustCompile(`^(@[a-z0-9][a-z0-9._~-]*/)?[a-z0-9][a-z0-9._~-]*$`)

// Service implements the npm registry on top of repo.Service.
type Service struct {
	db     *database.DB
	repos  *repository.Service
	log    *slog.Logger
	client *http.Client
	now    func() time.Time
}

// NewService returns a Service and registers its deploy hook, which indexes
// tarballs deployed by plain PUT into npm repositories.
func NewService(db *database.DB, repos *repository.Service, log *slog.Logger) *Service {
	s := &Service{
		db:    db,
		repos: repos,
		log:   log,
		now:   time.Now,
		client: &http.Client{
			Timeout: 60 * time.Second,
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
			},
		},
	}
	repos.RegisterMetadata(func(name string) bool {
		return strings.HasSuffix(name, packumentCacheSuffix)
	})
	repos.RegisterHook(Format, s.indexTarball)
	repos.SetUpstreamCheck(Format, s.checkTarball)

	return s
}

// ValidName reports whether name is a valid npm package name.
func ValidName(name string) bool { return len(name) <= 214 && validName.MatchString(name) }

// TarballPath is where a version's tarball is stored,
// <name>/-/<name>-<version>.tgz, which repeats the scope for scoped
// packages (@scope/pkg/-/@scope/pkg-1.0.0.tgz).
func TarballPath(name, version string) string {
	return name + "/-/" + name + "-" + version + ".tgz"
}

func (s *Service) indexTarball(ctx context.Context, r repository.Repository, item repository.Item) error {
	if !strings.HasSuffix(item.Name, ".tgz") {
		return nil
	}

	rc, err := s.repos.Open(ctx, item, nil)
	if err != nil {
		return err
	}

	defer func() {
		_ = rc.Close()
	}()

	h := sha512.New()

	manifest, err := readManifest(io.TeeReader(rc, h))
	if err != nil {
		return err
	}

	_, _ = io.Copy(h, rc)

	var head struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(manifest, &head); err != nil || !ValidName(head.Name) || head.Version == "" {
		return fmt.Errorf("%w: package.json lacks a valid name or version", ErrInvalid)
	}

	integrity := "sha512-" + base64.StdEncoding.EncodeToString(h.Sum(nil))

	return s.record(ctx, r.Name, item, head.Name, head.Version, manifest, integrity)
}

func (s *Service) record(
	ctx context.Context,
	repoKey string,
	item repository.Item,
	name, version string,
	manifest []byte,
	integrity string,
) error {
	err := s.db.UpsertNpmVersion(ctx, database.UpsertNpmVersionParams{
		ItemID:    item.ID,
		RepoKey:   repoKey,
		Name:      name,
		Version:   version,
		Manifest:  manifest,
		Integrity: integrity,
	})
	if err != nil {
		return fmt.Errorf("record %s@%s: %w", name, version, err)
	}

	return s.repos.SetProperties(ctx, item, repository.Properties{
		"npm.name": {
			name,
		},
		"npm.version": {
			version,
		},
	}, false)
}

func readManifest(r io.Reader) ([]byte, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("%w: not a gzip tarball: %w", ErrInvalid, err)
	}

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%w: tarball has no package.json", ErrInvalid)
		}

		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
		}

		parts := strings.Split(strings.TrimPrefix(hdr.Name, "./"), "/")
		if hdr.Typeflag == tar.TypeReg && len(parts) == 2 && parts[1] == "package.json" {
			b, err := io.ReadAll(io.LimitReader(tr, maxManifestBytes+1))
			if err != nil {
				return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
			}

			if len(b) > maxManifestBytes {
				return nil, fmt.Errorf("%w: package.json too large", ErrInvalid)
			}

			return b, nil
		}
	}
}

// Packument is an npm package document.
type Packument map[string]any

// Packument returns the document for name in repoKey.
func (s *Service) Packument(ctx context.Context, repoKey, name, tarballBase string) (Packument, error) {
	r, err := s.repos.GetRepository(ctx, repoKey)
	if err != nil {
		return nil, err
	}

	switch r.Class {
	case repository.ClassRemote:
		return s.remotePackument(ctx, r, name, tarballBase)
	case repository.ClassVirtual:
		return s.virtualPackument(ctx, r, name, tarballBase)
	default:
		return s.localPackument(ctx, repoKey, name, tarballBase)
	}
}

func (s *Service) localPackument(ctx context.Context, repoKey, name, tarballBase string) (Packument, error) {
	rows, err := s.db.ListNpmVersions(ctx, database.ListNpmVersionsParams{
		RepoKey: repoKey,
		Name:    name,
	})
	if err != nil {
		return nil, err
	}

	if len(rows) == 0 {
		return nil, ErrNotFound
	}

	versions := map[string]any{}
	times := map[string]any{}

	var (
		latest            string
		created, modified time.Time
	)

	for _, row := range rows {
		if !repository.Visible(ctx, repoKey, repository.JoinPath(row.ItemPath, row.ItemName)) {
			continue
		}

		var m map[string]any
		if err := json.Unmarshal(row.Manifest, &m); err != nil {
			continue
		}

		dist, _ := m["dist"].(map[string]any)
		if dist == nil {
			dist = map[string]any{}
		}

		dist["tarball"] = tarballBase + "/" + repository.JoinPath(row.ItemPath, row.ItemName)
		if row.Sha1 != nil {
			dist["shasum"] = *row.Sha1
		}

		if row.Integrity != "" {
			dist["integrity"] = row.Integrity
		}

		m["dist"], m["_id"] = dist, name+"@"+row.Version
		versions[row.Version] = m
		times[row.Version] = row.Created.UTC().Format(time.RFC3339Nano)
		latest = row.Version

		if created.IsZero() {
			created = row.Created
		}

		modified = row.Created
	}

	if len(versions) == 0 {
		return nil, ErrNotFound
	}

	tags := map[string]any{}

	tagRows, err := s.db.ListNpmDistTags(ctx, database.ListNpmDistTagsParams{
		RepoKey: repoKey,
		Name:    name,
	})
	if err != nil {
		return nil, err
	}

	for _, t := range tagRows {
		if _, ok := versions[t.Version]; ok {
			tags[t.Tag] = t.Version
		}
	}

	if _, ok := tags["latest"]; !ok {
		tags["latest"] = latest
	}

	times["created"] = created.UTC().Format(time.RFC3339Nano)
	times["modified"] = modified.UTC().Format(time.RFC3339Nano)

	doc := Packument{
		"_id":       name,
		"name":      name,
		"dist-tags": tags,
		"versions":  versions,
		"time":      times,
	}
	if m, ok := versions[tags["latest"].(string)].(map[string]any); ok {
		for _, k := range []string{
			"description",
			"readme",
			"license",
			"homepage",
			"repository",
			"keywords",
			"author",
		} {
			if v, ok := m[k]; ok {
				doc[k] = v
			}
		}
	}

	return doc, nil
}

func (s *Service) remotePackument(
	ctx context.Context,
	r repository.Repository,
	name, tarballBase string,
) (Packument, error) {
	cfg := r.Upstream()
	if cfg.URL == "" {
		return nil, ErrNotFound
	}

	upstream := strings.TrimSuffix(cfg.URL, "/")

	doc, err := s.cachedPackument(ctx, r, upstream+"/"+escapeName(name), name)
	if err != nil {
		return nil, err
	}

	versions, _ := doc["versions"].(map[string]any)
	paths := make(map[string]string, len(versions))

	for ver, v := range versions {
		m, _ := v.(map[string]any)
		dist, _ := m["dist"].(map[string]any)

		if attestations, ok := dist["attestations"].(map[string]any); ok {
			attestations["url"] = tarballBase + "/-/npm/v1/attestations/" + escapeName(name) + "@" + ver
		}

		tarball, _ := dist["tarball"].(string)
		if tarball == "" {
			continue
		}

		rel, ok := strings.CutPrefix(tarball, upstream+"/")
		if !ok {
			u, err := url.Parse(tarball)
			if err != nil {
				continue
			}

			rel = strings.TrimPrefix(u.Path, "/")
		}

		if !repository.Visible(ctx, r.Name, rel) {
			delete(versions, ver)
			continue
		}

		paths[ver] = rel
		dist["tarball"] = tarballBase + "/" + rel
	}

	if cooldown := cfg.Cooldown(); cooldown > 0 {
		coolDown(doc, s.now().Add(-cooldown))
	}

	offered := make([]repository.Version, 0, len(versions))
	for ver := range versions {
		offered = append(offered, repository.Version{
			Version: ver,
			Path:    paths[ver],
		})
	}

	refused, err := s.repos.RefusedVersions(ctx, r, "npm", name, offered)
	if err != nil {
		return nil, err
	}

	if len(refused) > 0 {
		dropVersions(doc, func(version string) bool {
			return refused[version]
		})
	}

	if repository.Restricted(ctx, r.Name) {
		if len(versions) == 0 {
			return nil, ErrNotFound
		}

		dropMissing(doc, versions)
	}

	return doc, nil
}

const maxPackumentBytes = 256 << 20

const packumentCacheSuffix = ".npm.json"

func (s *Service) cachedPackument(
	ctx context.Context,
	r repository.Repository,
	upstreamURL, name string,
) (Packument, error) {
	item, err := s.repos.ResolveURL(ctx, r, upstreamURL, ".npm/"+name+packumentCacheSuffix)
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

	var doc Packument
	if err := json.NewDecoder(io.LimitReader(rc, maxPackumentBytes)).Decode(&doc); err != nil {
		return nil, fmt.Errorf("%w: invalid packument: %w", repository.ErrUpstream, err)
	}

	return doc, nil
}

func dropMissing(doc Packument, versions map[string]any) {
	for _, key := range []string{
		"dist-tags",
		"time",
	} {
		m, _ := doc[key].(map[string]any)
		for k, v := range m {
			ver, _ := v.(string)
			if key == "time" {
				ver = k
				if k == "created" || k == "modified" {
					continue
				}
			}

			if _, ok := versions[ver]; !ok {
				delete(m, k)
			}
		}
	}
}

func (s *Service) virtualPackument(
	ctx context.Context,
	r repository.Repository,
	name, tarballBase string,
) (Packument, error) {
	merged := Packument{
		"_id":  name,
		"name": name,
	}
	versions, tags, times := map[string]any{}, map[string]any{}, map[string]any{}
	found := false

	for _, member := range s.repos.Flatten(ctx, r, name) {
		doc, err := s.Packument(ctx, member.Name, name, tarballBase)
		if errors.Is(err, ErrNotFound) || errors.Is(err, repository.ErrRepositoryNotFound) ||
			errors.Is(err, repository.ErrUpstream) {
			continue
		}

		if err != nil {
			return nil, err
		}

		found = true

		for k, v := range doc {
			if _, ok := merged[k]; !ok && k != "versions" && k != "dist-tags" && k != "time" {
				merged[k] = v
			}
		}

		mergeMissing(versions, doc["versions"])
		mergeMissing(tags, doc["dist-tags"])
		mergeMissing(times, doc["time"])
	}

	if !found {
		return nil, ErrNotFound
	}

	merged["versions"], merged["dist-tags"], merged["time"] = versions, tags, times

	return merged, nil
}

func mergeMissing(dst map[string]any, src any) {
	m, _ := src.(map[string]any)
	for k, v := range m {
		if _, ok := dst[k]; !ok {
			dst[k] = v
		}
	}
}

func escapeName(name string) string {
	return strings.Replace(url.PathEscape(name), "%40", "@", 1)
}

// PublishDocument is the body of `npm publish` (PUT /<name>).
type PublishDocument struct {
	Name        string                     `json:"name"`
	Versions    map[string]json.RawMessage `json:"versions"`
	DistTags    map[string]string          `json:"dist-tags"`
	Attachments map[string]struct {
		Data   string `json:"data"`
		Length int64  `json:"length"`
	} `json:"_attachments"`
}

// UpdateMetadata applies a publish document without attachments: what
// npm deprecate sends (the full packument with "deprecated" set, or set to
// "" to undo it, on the versions it changes).
func (s *Service) UpdateMetadata(ctx context.Context, repoKey string, doc PublishDocument) error {
	if !ValidName(doc.Name) {
		return fmt.Errorf("%w: invalid package name", ErrInvalid)
	}

	updated := false

	for version, raw := range doc.Versions {
		var m struct {
			Deprecated *string `json:"deprecated"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			return fmt.Errorf("%w: version %s: %w", ErrInvalid, version, err)
		}

		message := ""
		if m.Deprecated != nil {
			message = *m.Deprecated
		}

		n, err := s.db.SetNpmDeprecated(
			ctx,
			database.SetNpmDeprecatedParams{
				Message: message,
				RepoKey: repoKey,
				Name:    doc.Name,
				Version: version,
			},
		)
		if err != nil {
			return err
		}

		updated = updated || n > 0
	}

	if !updated {
		return ErrNotFound
	}

	return nil
}

// Publish stores the versions and tarballs of a publish document in a local
// repository.
func (s *Service) Publish(ctx context.Context, repoKey string, doc PublishDocument, user string) error {
	if !ValidName(doc.Name) || len(doc.Versions) == 0 {
		return fmt.Errorf("%w: a publish needs a valid name and at least one version", ErrInvalid)
	}

	type publishedVersion struct {
		version   string
		raw       json.RawMessage
		tarball   []byte
		integrity string
	}

	published := make([]publishedVersion, 0, len(doc.Versions))

	for version, raw := range doc.Versions {
		exists, err := s.db.NpmVersionExists(
			ctx,
			database.NpmVersionExistsParams{
				RepoKey: repoKey,
				Name:    doc.Name,
				Version: version,
			},
		)
		if err != nil {
			return err
		}

		if exists {
			return fmt.Errorf("%w: %s@%s", ErrVersionExists, doc.Name, version)
		}

		var m struct {
			Dist struct {
				Tarball   string `json:"tarball"`
				Integrity string `json:"integrity"`
			} `json:"dist"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			return fmt.Errorf("%w: version %s: %w", ErrInvalid, version, err)
		}

		att, ok := doc.Attachments[attachmentName(doc, m.Dist.Tarball, version)]
		if !ok {
			return fmt.Errorf("%w: no tarball attached for %s", ErrInvalid, version)
		}

		data, err := base64.StdEncoding.DecodeString(att.Data)
		if err != nil {
			return fmt.Errorf("%w: attachment is not base64: %w", ErrInvalid, err)
		}

		sum := sha512.Sum512(data)
		integrity := "sha512-" + base64.StdEncoding.EncodeToString(sum[:])

		for _, claimed := range strings.Fields(m.Dist.Integrity) {
			if strings.HasPrefix(claimed, "sha512-") && claimed != integrity {
				return fmt.Errorf("%w: the integrity of %s does not match its tarball", ErrInvalid, version)
			}
		}

		published = append(
			published,
			publishedVersion{
				version:   version,
				raw:       raw,
				tarball:   data,
				integrity: integrity,
			},
		)
	}

	for _, pv := range published {
		item, err := s.repos.Deploy(
			ctx,
			repository.DeployInput{
				Repository: repoKey,
				Path:       TarballPath(doc.Name, pv.version),
				Body:       bytes.NewReader(pv.tarball),
				User:       user,
			},
		)
		if err != nil {
			return err
		}

		if err := s.record(ctx, repoKey, item, doc.Name, pv.version, pv.raw, pv.integrity); err != nil {
			return err
		}
	}

	for tag, version := range doc.DistTags {
		if err := s.pointTag(ctx, repoKey, doc.Name, tag, version); err != nil {
			return err
		}
	}

	return nil
}

func attachmentName(doc PublishDocument, tarball, version string) string {
	if len(doc.Attachments) == 1 {
		for k := range doc.Attachments {
			return k
		}
	}

	for _, candidate := range []string{
		doc.Name + "-" + version + ".tgz",
		tarball[strings.LastIndex(tarball, "/")+1:],
	} {
		if _, ok := doc.Attachments[candidate]; ok {
			return candidate
		}
	}

	for k := range doc.Attachments {
		if strings.HasSuffix(k, "-"+version+".tgz") {
			return k
		}
	}

	return ""
}

// SetTag points a dist-tag at a published version.
func (s *Service) SetTag(ctx context.Context, repoKey, name, tag, version string) error {
	if tag == "" || version == "" {
		return errTagWithoutVersion
	}

	exists, err := s.db.NpmVersionExists(
		ctx,
		database.NpmVersionExistsParams{
			RepoKey: repoKey,
			Name:    name,
			Version: version,
		},
	)
	if err != nil {
		return err
	}

	if !exists {
		return fmt.Errorf("%w: %s@%s", ErrNotFound, name, version)
	}

	return s.pointTag(ctx, repoKey, name, tag, version)
}

var errTagWithoutVersion = fmt.Errorf("%w: tag and version are required", ErrInvalid)

func (s *Service) pointTag(ctx context.Context, repoKey, name, tag, version string) error {
	if tag == "" || version == "" {
		return errTagWithoutVersion
	}

	return s.db.SetNpmDistTag(
		ctx,
		database.SetNpmDistTagParams{
			RepoKey: repoKey,
			Name:    name,
			Tag:     tag,
			Version: version,
		},
	)
}

// DeleteTag removes a dist-tag.
func (s *Service) DeleteTag(ctx context.Context, repoKey, name, tag string) error {
	if tag == "latest" {
		return fmt.Errorf("%w: the latest tag cannot be removed", ErrInvalid)
	}

	n, err := s.db.DeleteNpmDistTag(ctx, database.DeleteNpmDistTagParams{
		RepoKey: repoKey,
		Name:    name,
		Tag:     tag,
	})
	if err != nil {
		return err
	}

	if n == 0 {
		return ErrNotFound
	}

	return nil
}

// Unpublish moves the versions of name that keep rejects (all of them when keep is nil) to the trash can.
func (s *Service) Unpublish(ctx context.Context, repoKey, name, user string, keep func(version string) bool) error {
	rows, err := s.db.ListNpmVersions(ctx, database.ListNpmVersionsParams{
		RepoKey: repoKey,
		Name:    name,
	})
	if err != nil {
		return err
	}

	removed := map[string]bool{}

	for _, row := range rows {
		if keep != nil && keep(row.Version) {
			continue
		}

		if err := s.repos.DeleteToTrash(
			ctx,
			repoKey,
			repository.JoinPath(row.ItemPath, row.ItemName),
			user,
		); err != nil {
			return fmt.Errorf("unpublish %s@%s: %w", name, row.Version, err)
		}

		removed[row.Version] = true
	}

	if len(removed) == 0 {
		return ErrNotFound
	}

	tags, err := s.db.ListNpmDistTags(ctx, database.ListNpmDistTagsParams{
		RepoKey: repoKey,
		Name:    name,
	})
	if err != nil {
		return err
	}

	for _, tag := range tags {
		if !removed[tag.Version] {
			continue
		}

		if _, err := s.db.DeleteNpmDistTag(ctx, database.DeleteNpmDistTagParams{
			RepoKey: repoKey,
			Name:    name,
			Tag:     tag.Tag,
		}); err != nil {
			return err
		}
	}

	return nil
}

// SearchResult is one npm search hit.
type SearchResult struct {
	Name        string
	Version     string
	Description string
	Keywords    []string
	Maintainers []Person
	Publisher   string
	Date        time.Time
}

// Person is an npm user a package's metadata names.
type Person struct {
	Name  string
	Email string
}

// Search finds packages whose name contains every word of text in the
// given repositories.
func (s *Service) Search(ctx context.Context, repoKeys []string, text string, limit int32) ([]SearchResult, error) {
	patterns := searchPatterns(text)
	if slices.ContainsFunc(repoKeys, func(k string) bool {
		return repository.Restricted(ctx, k)
	}) {
		return s.searchVisible(ctx, repoKeys, patterns, limit)
	}

	rows, err := s.db.SearchNpmPackages(
		ctx,
		database.SearchNpmPackagesParams{
			RepoKeys: repoKeys,
			Patterns: patterns,
			Max:      limit,
		},
	)
	if err != nil {
		return nil, err
	}

	out := make([]SearchResult, len(rows))
	for i, r := range rows {
		out[i] = searchResult(r.Name, r.Version, r.Manifest, r.Created, r.Publisher)
	}

	return out, nil
}

func searchPatterns(text string) []string {
	escape := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	words := strings.Fields(text)

	patterns := make([]string, 0, len(words))
	for _, w := range words {
		patterns = append(patterns, "%"+escape.Replace(w)+"%")
	}

	return patterns
}

// searchResult is the search hit of a version's manifest.
func searchResult(name, version string, manifest []byte, created time.Time, publisher string) SearchResult {
	var m struct {
		Description string            `json:"description"`
		Keywords    json.RawMessage   `json:"keywords"`
		Maintainers []json.RawMessage `json:"maintainers"`
		NpmUser     json.RawMessage   `json:"_npmUser"`
	}

	_ = json.Unmarshal(manifest, &m)

	r := SearchResult{
		Name:        name,
		Version:     version,
		Description: m.Description,
		Keywords:    parseKeywords(m.Keywords),
		Publisher:   publisher,
		Date:        created,
	}

	for _, raw := range m.Maintainers {
		if p, ok := parsePerson(raw); ok {
			r.Maintainers = append(r.Maintainers, p)
		}
	}

	if p, ok := parsePerson(m.NpmUser); ok {
		r.Publisher = p.Name
	}

	if r.Publisher == repository.SystemUser {
		r.Publisher = ""
	}

	if len(r.Maintainers) == 0 && r.Publisher != "" {
		r.Maintainers = []Person{
			{
				Name: r.Publisher,
			},
		}
	}

	return r
}

var personText = regexp.MustCompile(`^\s*([^<(]*?)\s*(?:<([^>]*)>)?\s*(?:\([^)]*\))?\s*$`)

// parsePerson reads a person in either form package.json allows: an
// object with a name and an email, or "Name <email> (url)".
func parsePerson(raw json.RawMessage) (Person, bool) {
	var p struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	if json.Unmarshal(raw, &p) == nil && p.Name != "" {
		return Person{
			Name:  p.Name,
			Email: p.Email,
		}, true
	}

	var text string
	if json.Unmarshal(raw, &text) != nil {
		return Person{}, false
	}

	match := personText.FindStringSubmatch(text)
	if match == nil || match[1] == "" {
		return Person{}, false
	}

	return Person{
		Name:  match[1],
		Email: match[2],
	}, true
}

const maxSearchVersions = 10_000

func (s *Service) searchVisible(
	ctx context.Context,
	repoKeys []string,
	patterns []string,
	limit int32,
) ([]SearchResult, error) {
	rows, err := s.db.SearchNpmVersions(
		ctx,
		database.SearchNpmVersionsParams{
			RepoKeys: repoKeys,
			Patterns: patterns,
			Max:      maxSearchVersions,
		},
	)
	if err != nil {
		return nil, err
	}

	var out []SearchResult
	for _, r := range rows {
		if len(out) > 0 && out[len(out)-1].Name == r.Name ||
			!repository.Visible(ctx, r.RepoKey, repository.JoinPath(r.ItemPath, r.ItemName)) {
			continue
		}

		if int32(len(out)) >= limit { //nolint:gosec // G115: len is far below MaxInt32
			break
		}

		out = append(out, searchResult(r.Name, r.Version, r.Manifest, r.Created, r.Publisher))
	}

	return out, nil
}

// ErrNoAuditSource is returned when a repository has no remote to answer
// npm audit.
var ErrNoAuditSource = errors.New("no npm remote to answer audit requests")

// MaxAuditBody is the largest npm audit request forwarded upstream.
const MaxAuditBody = 32 << 20

// Audit forwards an npm audit request (-/npm/v1/security/...) to the
// upstream of r, or of the first npm remote of a virtual, as a proxying
// remote npm repository does: the public registry answers with its advisory
// database.
func (s *Service) Audit(
	ctx context.Context,
	r repository.Repository,
	apiPath string,
	body io.Reader,
	header http.Header,
) (*http.Response, error) {
	remote, ok := s.onlineRemote(ctx, r, 0)
	if !ok {
		return nil, ErrNoAuditSource
	}

	cfg := remote.Upstream()

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		strings.TrimSuffix(cfg.URL, "/")+"/"+apiPath,
		io.LimitReader(body, MaxAuditBody),
	)
	if err != nil {
		return nil, err
	}

	for _, h := range []string{
		"Content-Type",
		"Content-Encoding",
		"Accept",
	} {
		if v := header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}

	req.Header.Set("User-Agent", repository.UserAgent)

	if cfg.Username != "" {
		req.SetBasicAuth(cfg.Username, cfg.Password)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", repository.ErrUpstream, err)
	}

	return resp, nil
}

func (s *Service) onlineRemote(ctx context.Context, r repository.Repository, depth int) (repository.Repository, bool) {
	switch {
	case r.Class == "remote":
		return r, !s.repos.UpstreamOf(r).Offline
	case r.Class != "virtual" || depth >= repository.MaxVirtualDepth:
		return repository.Repository{}, false
	}

	for _, key := range r.Members() {
		if m, err := s.repos.GetRepository(ctx, key); err == nil {
			if remote, ok := s.onlineRemote(ctx, m, depth+1); ok {
				return remote, true
			}
		}
	}

	return repository.Repository{}, false
}

const maxRegistryDocumentBytes = 16 << 20

// SigningKeys returns the signing keys of the upstream registry of r's first online npm remote.
func (s *Service) SigningKeys(ctx context.Context, r repository.Repository) ([]byte, error) {
	return s.registryDocument(ctx, r, "-/npm/v1/keys")
}

// Attestations returns the provenance attestations of a version from the same upstream registry.
func (s *Service) Attestations(ctx context.Context, r repository.Repository, name, version string) ([]byte, error) {
	if !ValidName(name) || !validVersion.MatchString(version) {
		return nil, fmt.Errorf("%w: invalid package version", ErrInvalid)
	}

	return s.registryDocument(ctx, r, "-/npm/v1/attestations/"+escapeName(name)+"@"+version)
}

var validVersion = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+-]*$`)

func (s *Service) registryDocument(ctx context.Context, r repository.Repository, apiPath string) ([]byte, error) {
	remote, ok := s.onlineRemote(ctx, r, 0)
	if !ok {
		return nil, ErrNotFound
	}

	upstream := strings.TrimSuffix(remote.Upstream().URL, "/")

	body, err := s.repos.FetchUpstream(ctx, remote, upstream+"/"+apiPath, "application/json", maxRegistryDocumentBytes)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, ErrNotFound
	}

	return body, err
}

// Offers reports whether repoKey's metadata for name offers version: a
// remote or virtual repository leaves out versions cooling down,
// quarantined, known malicious or blocked.
func (s *Service) Offers(ctx context.Context, repoKey, name, version string) (bool, error) {
	doc, err := s.Packument(ctx, repoKey, name, "")
	if errors.Is(err, ErrNotFound) || errors.Is(err, repository.ErrNotFound) {
		return false, nil
	}

	if err != nil {
		return false, err
	}

	versions, _ := doc["versions"].(map[string]any)
	_, ok := versions[version]

	return ok, nil
}
