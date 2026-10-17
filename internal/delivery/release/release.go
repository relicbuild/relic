// Package release implements releases:
// immutable, named and versioned sets of artifacts, promoted between
// environments, exported and imported as archives.
package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"
	"uuid"

	"relic/internal/access/project"
	"relic/internal/artifacts/itemquery"
	"relic/internal/artifacts/repository"
	"relic/internal/artifacts/signingkey"
	"relic/internal/delivery/buildinfo"
	"relic/internal/delivery/evidence"
	"relic/internal/platform/database"
	"relic/internal/platform/events"
	"relic/internal/platform/telemetry"
	"relic/internal/scanning/scan/policy"
)

// Domain errors.
var (
	ErrNotFound = errors.New("release not found")
	ErrExists   = errors.New("release version already exists")
	ErrInvalid  = errors.New("invalid release request")
	ErrNotDraft = errors.New("release version is not a draft")
	// ErrForbidden is content the caller may not read, or a promotion
	// target they may not write.
	ErrForbidden = errors.New("not permitted")
)

// Status is where a release version or a promotion stands.
type Status string

// Statuses.
const (
	StatusProcessing Status = "processing"
	StatusCompleted  Status = "completed"
	StatusDraft      Status = "draft"
	StatusFailed     Status = "failed"
)

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)

// DefaultProject is the project of releases outside any project; "" means
// the same.
const DefaultProject = project.DefaultKey

// Layout names where release versions are stored: the storing repository
// per project and the manifest file in each version folder.
type Layout struct {
	// DefaultRepository stores releases outside projects.
	DefaultRepository string
	// ProjectSuffix names a project's storing repository:
	// <project><ProjectSuffix>.
	ProjectSuffix string
	ManifestName  string
}

// DefaultLayout is Relic's own.
var DefaultLayout = Layout{
	DefaultRepository: "relic-releases",
	ProjectSuffix:     "-relic-releases",
	ManifestName:      "manifest.json",
}

// Repository is the storing repository for a project.
func (l Layout) Repository(project string) string {
	if isDefaultProject(project) {
		return l.DefaultRepository
	}

	return project + l.ProjectSuffix
}

func isDefaultProject(key string) bool { return project.IsDefault(key) }

func ownerProject(key string) string {
	if isDefaultProject(key) {
		return ""
	}

	return key
}

// StorageOwner reports whether name is a storing repository, and the project
// ("" outside projects) whose releases it stores.
func (l Layout) StorageOwner(name string) (repository.Format, string, bool) {
	if name == l.DefaultRepository {
		return repository.FormatRelease, "", true
	}

	key, ok := strings.CutSuffix(name, l.ProjectSuffix)
	if !ok || isDefaultProject(key) || !project.ValidKey(key) {
		return "", "", false
	}

	return repository.FormatRelease, key, true
}

// SetLayout sets where release versions are stored.
func (s *Service) SetLayout(l Layout) { s.layout = l }

// Layout is where release versions are stored.
func (s *Service) Layout() Layout { return s.layout }

// StorageOwner reports whether name is a storing repository, and the project
// whose releases it stores.
func (s *Service) StorageOwner(name string) (repository.Format, string, bool) {
	return s.layout.StorageOwner(name)
}

// Artifact is one file in a release version. Its JSON form is stored with
// the version and written into export archives and manifests.
type Artifact struct {
	Repository string `json:"repository"` // the source repository
	Path       string `json:"path"`       // path in the source repository
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
}

// Release is a stored release version.
type Release struct {
	ID         uuid.UUID
	Project    string
	Repository string // the storing repository
	Name       string
	Version    string
	Status     Status
	Draft      bool
	Tag        string
	Artifacts  []Artifact
	CreatedBy  string
	Created    time.Time
	Versions   int64 // number of versions; set by Groups only
}

// Folder is the release version's folder in its storing repository.
func (b Release) Folder() string { return b.Name + "/" + b.Version }

// ManifestPath is the path of b's signed manifest in its storing repository.
func (s *Service) ManifestPath(b Release) string { return b.Folder() + "/" + s.layout.ManifestName }

func (b Release) artifactPath(a Artifact) string { return b.Folder() + "/artifacts/" + a.Path }

// Service manages releases.
type Service struct {
	db     *database.DB
	repos  *repository.Service
	builds *buildinfo.Service
	layout Layout
	log    *slog.Logger
	now    func() time.Time
	keys   *signingkey.Service
	// evidence records gate decisions; nil records none.
	evidence *evidence.Service
	// decisions records refused promotions; nil records none.
	decisions *policy.Decisions
	events    events.Publisher
	// projects checks that a release's project exists; nil checks nothing.
	projects *project.Service
	// refuseFile refuses a file whose download is blocked; nil refuses none.
	refuseFile func(ctx context.Context, file repository.Item) error
}

// SetProjects makes releases refuse projects that do not exist.
func (s *Service) SetProjects(p *project.Service) { s.projects = p }

// SetEvents publishes promotions to p.
func (s *Service) SetEvents(p events.Publisher) { s.events = p }

// SetDownloadBlocker keeps files out of releases, promotions and exports
// when refuse returns an error for them, as it does for a blocked download.
func (s *Service) SetDownloadBlocker(refuse func(ctx context.Context, file repository.Item) error) {
	s.refuseFile = refuse
}

// New returns a Service.
func New(
	db *database.DB,
	repos *repository.Service,
	builds *buildinfo.Service,
	keys *signingkey.Service,
	log *slog.Logger,
) *Service {
	return &Service{
		db:     db,
		repos:  repos,
		builds: builds,
		keys:   keys,
		layout: DefaultLayout,
		log:    log,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

// Spec is a release version to create, or the sources to add to a draft.
type Spec struct {
	Name    string
	Version string
	Sources []Source
	Draft   bool
}

// SourceKind says where a source's artifacts come from.
type SourceKind string

// Source kinds.
const (
	SourceQuery     SourceKind = "query"     // an item query
	SourceArtifacts SourceKind = "artifacts" // files named by path
	SourceBuilds    SourceKind = "builds"    // build runs' artifacts
	SourceReleases  SourceKind = "releases"  // other release versions
)

// Source is one source of artifacts.
type Source struct {
	Kind      SourceKind
	Query     itemquery.Query
	Artifacts []ArtifactRef
	Builds    []BuildRef
	Releases  []Ref
}

// ArtifactRef names one file as "repository/path", optionally with the
// SHA-256 it must have.
type ArtifactRef struct {
	Path   string
	SHA256 string
}

// BuildRef names a build run.
type BuildRef struct {
	Name         string
	Number       string
	Project      string // "" for builds outside projects
	Dependencies bool
}

// Ref names another release version.
type Ref struct {
	Name    string
	Version string
	// Project is the release's project: "" for the new version's own
	// project, DefaultProject for releases outside projects.
	Project string
}

// Access limits what a caller may read into a bundle and write when
// promoting one.
type Access struct {
	CanRead   func(repository, path string) bool
	CanWrite  func(repository, path string) bool
	CanDelete func(repository, path string) bool
	// CanReadBuild says whether the caller may read the build name stored
	// in the build-info repository.
	CanReadBuild func(repository, name string) bool
}

func (a Access) canRead(repository, path string) bool {
	return a.CanRead == nil || a.CanRead(repository, path)
}

func (a Access) canWrite(repository, path string) bool {
	return a.CanWrite == nil || a.CanWrite(repository, path)
}

func (a Access) canDelete(repository, path string) bool {
	return a.CanDelete == nil || a.CanDelete(repository, path)
}

func (a Access) canReadBuild(repository, name string) bool {
	return a.CanReadBuild == nil || a.CanReadBuild(repository, name)
}

// Create resolves the request's sources and stores the new bundle version.
func (s *Service) Create(
	ctx context.Context,
	project string,
	req Spec,
	user string,
	access Access,
) (_ Release, err error) {
	ctx, done := telemetry.Operation(ctx, "release.Create", func(err error) {
		telemetry.ReleaseOp("create", err)
	})
	defer func() {
		done(err)
	}()

	if !namePattern.MatchString(req.Name) || !namePattern.MatchString(req.Version) {
		return Release{}, fmt.Errorf("%w: invalid name or version", ErrInvalid)
	}

	entries, err := s.resolve(ctx, project, req.Sources, access)
	if err != nil {
		return Release{}, err
	}

	if err := readable(entries, access); err != nil {
		return Release{}, err
	}

	b := Release{
		ID:         uuid.NewV7(),
		Project:    project,
		Repository: s.layout.Repository(project),
		Name:       req.Name,
		Version:    req.Version,
		Status:     StatusProcessing,
		Draft:      req.Draft,
		CreatedBy:  user,
		Created:    s.now(),
	}
	if err := s.ensureRepo(ctx, b.Repository, project); err != nil {
		return Release{}, err
	}

	err = s.db.WithLock(ctx, releaseLock(project, req.Name, req.Version), func() error {
		if err := s.insert(ctx, b); err != nil {
			return err
		}

		if err := s.materialize(ctx, &b, entries, user); err != nil {
			s.discard(ctx, b)
			return err
		}

		return nil
	})
	if err != nil {
		return Release{}, err
	}

	return b, nil
}

func readable(entries []entry, access Access) error {
	for _, e := range entries {
		if !access.canRead(e.item.Repository, e.item.Path()) {
			return fmt.Errorf("%w: cannot read %s/%s", ErrForbidden, e.item.Repository, e.item.Path())
		}
	}

	return nil
}

// Update adds sources to a draft.
func (s *Service) Update(
	ctx context.Context,
	project string,
	req Spec,
	user string,
	access Access,
) (_ Release, err error) {
	ctx, done := telemetry.Operation(ctx, "release.Update", func(err error) {
		telemetry.ReleaseOp("update", err)
	})
	defer func() {
		done(err)
	}()

	var b Release

	err = s.db.WithLock(ctx, releaseLock(project, req.Name, req.Version), func() error {
		if b, err = s.Get(ctx, project, req.Name, req.Version); err != nil {
			return err
		}

		if !b.Draft {
			return ErrNotDraft
		}

		entries, err := s.resolve(ctx, project, req.Sources, access)
		if err != nil {
			return err
		}

		if err := readable(entries, access); err != nil {
			return err
		}

		recorded := len(b.Artifacts)
		if err := s.materialize(ctx, &b, entries, user); err != nil {
			for _, a := range b.Artifacts[recorded:] {
				if err := s.repos.Delete(ctx, b.Repository, b.artifactPath(a)); err != nil &&
					!errors.Is(err, repository.ErrNotFound) {
					s.log.WarnContext(ctx, "clean up failed release update", "release", b.Folder(), "error", err)
				}
			}

			return err
		}

		return nil
	})
	if err != nil {
		return Release{}, err
	}

	return b, nil
}

// Finalize turns a draft into a completed, immutable version.
func (s *Service) Finalize(ctx context.Context, project, name, version, user string) (_ Release, err error) {
	ctx, done := telemetry.Operation(ctx, "release.Finalize", func(err error) {
		telemetry.ReleaseOp("finalize", err)
	})
	defer func() {
		done(err)
	}()

	var b Release

	err = s.db.WithLock(ctx, releaseLock(project, name, version), func() error {
		if b, err = s.Get(ctx, project, name, version); err != nil {
			return err
		}

		if !b.Draft {
			return ErrNotDraft
		}

		b.Draft = false

		return s.materialize(ctx, &b, nil, user)
	})
	if err != nil {
		return Release{}, err
	}

	return b, nil
}

// releaseLock serializes the creation and changes of one release version.
func releaseLock(project, name, version string) string {
	return "release:" + project + "/" + name + "/" + version
}

func (s *Service) materialize(ctx context.Context, b *Release, entries []entry, user string) error {
	have := make(map[string]string, len(b.Artifacts))
	for _, a := range b.Artifacts {
		have[a.Path] = a.SHA256
	}

	for _, e := range entries {
		if sha, ok := have[e.art.Path]; ok {
			if sha != e.art.SHA256 {
				return fmt.Errorf("%w: %s is already in the bundle with different content", ErrInvalid, e.art.Path)
			}

			continue
		}

		err := s.repos.Copy(
			ctx,
			repository.CopyInput{
				Source:                e.item,
				DestinationRepository: b.Repository,
				DestinationPath:       b.artifactPath(e.art),
				User:                  user,
				RefuseFile:            s.refuseFile,
			},
		)
		if err != nil {
			return fmt.Errorf("copy %s/%s into the bundle: %w", e.item.Repository, e.item.Path(), err)
		}

		have[e.art.Path] = e.art.SHA256
		b.Artifacts = append(b.Artifacts, e.art)
	}

	if len(b.Artifacts) == 0 && !b.Draft {
		return fmt.Errorf("%w: the sources resolved to no artifacts", ErrInvalid)
	}

	manifest, err := s.signedManifest(ctx, *b)
	if err != nil {
		return err
	}

	if _, err := s.repos.Deploy(
		ctx,
		repository.DeployInput{
			Repository: b.Repository,
			Path:       b.Folder() + "/" + s.layout.ManifestName,
			Body:       strings.NewReader(string(manifest)),
			User:       user,
		},
	); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}

	return s.recordContent(ctx, b)
}

func (s *Service) insert(ctx context.Context, b Release) error {
	err := s.db.CreateRelease(ctx, database.CreateReleaseParams{
		ID:         b.ID,
		Project:    b.Project,
		Repository: b.Repository,
		Name:       b.Name,
		Version:    b.Version,
		Status:     string(b.Status),
		Draft:      b.Draft,
		Artifacts:  []byte("[]"),
		CreatedBy:  b.CreatedBy,
		Created:    b.Created,
	})
	if database.IsUniqueViolation(err) {
		return ErrExists
	}

	if err != nil {
		return fmt.Errorf("create release: %w", err)
	}

	return nil
}

func (s *Service) recordContent(ctx context.Context, b *Release) error {
	arts, err := json.Marshal(nonNil(b.Artifacts))
	if err != nil {
		return fmt.Errorf("encode artifacts: %w", err)
	}

	b.Status = StatusCompleted
	if b.Draft {
		b.Status = StatusDraft
	}

	err = s.db.UpdateReleaseContent(
		ctx,
		database.UpdateReleaseContentParams{
			ID:        b.ID,
			Artifacts: arts,
			Draft:     b.Draft,
			Status:    string(b.Status),
		},
	)
	if err != nil {
		return fmt.Errorf("record release content: %w", err)
	}

	return nil
}

func (s *Service) discard(ctx context.Context, b Release) {
	if err := s.repos.Delete(ctx, b.Repository, b.Folder()); err != nil && !errors.Is(err, repository.ErrNotFound) {
		s.log.WarnContext(ctx, "clean up failed release", "release", b.Folder(), "error", err)
	}

	if err := s.db.DeleteRelease(ctx, b.ID); err != nil {
		s.log.WarnContext(ctx, "clean up failed release", "release", b.Folder(), "error", err)
	}
}

func (s *Service) ensureRepo(ctx context.Context, key, projectKey string) error {
	if s.projects != nil && projectKey != "" && projectKey != DefaultProject {
		_, err := s.projects.Get(ctx, projectKey)
		if errors.Is(err, project.ErrNotFound) {
			return fmt.Errorf("%w: project %s does not exist", ErrInvalid, projectKey)
		}

		if err != nil {
			return err
		}
	}

	format, owner, _ := s.layout.StorageOwner(key)

	existing, err := s.repos.GetRepository(ctx, key)
	if err == nil {
		if existing.Format != format ||
			ownerProject(existing.Project) != owner {
			return fmt.Errorf("%w: %s is not the release repository of project %q", ErrInvalid, key, projectKey)
		}

		return nil
	}

	if !errors.Is(err, repository.ErrRepositoryNotFound) {
		return fmt.Errorf("get repository %s: %w", key, err)
	}

	r := repository.Repository{
		Name:    key,
		Class:   repository.ClassLocal,
		Format:  format,
		Project: owner,
	}
	if err := s.repos.CreateRepository(ctx, r); err != nil && !errors.Is(err, repository.ErrRepositoryExists) {
		return err
	}

	return nil
}

// Get returns one release version.
func (s *Service) Get(ctx context.Context, project, name, version string) (Release, error) {
	row, err := s.db.GetRelease(
		ctx,
		database.GetReleaseParams{
			Repository: s.layout.Repository(project),
			Name:       name,
			Version:    version,
		},
	)
	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		return Release{}, ErrNotFound
	}

	if err != nil {
		return Release{}, fmt.Errorf("get release %s/%s: %w", name, version, err)
	}

	return releaseFromRow(row)
}

// Versions lists a release's versions, newest first.
func (s *Service) Versions(ctx context.Context, project, name string) ([]Release, error) {
	rows, err := s.db.ListReleaseVersions(
		ctx,
		database.ListReleaseVersionsParams{
			Repository: s.layout.Repository(project),
			Name:       name,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("list release versions: %w", err)
	}

	out := make([]Release, 0, len(rows))
	for _, r := range rows {
		b, err := releaseFromRow(r)
		if err != nil {
			return nil, err
		}

		out = append(out, b)
	}

	return out, nil
}

// Groups lists every release name with its latest version.
func (s *Service) Groups(ctx context.Context) ([]Release, error) {
	rows, err := s.db.ListReleaseGroups(ctx)
	if err != nil {
		return nil, fmt.Errorf("list releases: %w", err)
	}

	out := make([]Release, 0, len(rows))
	for _, r := range rows {
		out = append(out, Release{
			ID:         r.ID,
			Project:    r.Project,
			Repository: r.Repository,
			Name:       r.Name,
			Version:    r.Version,
			Status:     Status(r.Status),
			Draft:      r.Draft,
			Tag:        r.Tag,
			CreatedBy:  r.CreatedBy,
			Created:    r.Created,
			Versions:   r.VersionsCount,
		})
	}

	return out, nil
}

// Delete removes a release version, its promotion records, its folder and its
// export archive.
func (s *Service) Delete(ctx context.Context, project, name, version string) (err error) {
	ctx, done := telemetry.Operation(ctx, "release.Delete", func(err error) {
		telemetry.ReleaseOp("delete", err)
	})
	defer func() {
		done(err)
	}()

	b, err := s.Get(ctx, project, name, version)
	if err != nil {
		return err
	}

	for _, p := range []string{
		b.Folder(),
		exportFolder(b),
	} {
		err := s.repos.Delete(ctx, b.Repository, p)
		if err != nil && !errors.Is(err, repository.ErrNotFound) && !errors.Is(err, repository.ErrRepositoryNotFound) {
			return fmt.Errorf("delete %s: %w", p, err)
		}
	}

	if err := s.db.DeleteRelease(ctx, b.ID); err != nil {
		return fmt.Errorf("delete release: %w", err)
	}

	return nil
}

// SetTag sets or, with an empty tag, clears a version's tag.
func (s *Service) SetTag(ctx context.Context, project, name, version, tag string) error {
	b, err := s.Get(ctx, project, name, version)
	if err != nil {
		return err
	}

	if err := s.db.SetReleaseTag(ctx, database.SetReleaseTagParams{
		ID:  b.ID,
		Tag: tag,
	}); err != nil {
		return fmt.Errorf("tag release: %w", err)
	}

	return nil
}

func releaseFromRow(r database.Release) (Release, error) {
	b := Release{
		ID:         r.ID,
		Project:    r.Project,
		Repository: r.Repository,
		Name:       r.Name,
		Version:    r.Version,
		Status:     Status(r.Status),
		Draft:      r.Draft,
		Tag:        r.Tag,
		CreatedBy:  r.CreatedBy,
		Created:    r.Created,
	}
	if err := json.Unmarshal(r.Artifacts, &b.Artifacts); err != nil {
		return Release{}, fmt.Errorf("decode release %s/%s artifacts: %w", r.Name, r.Version, err)
	}

	return b, nil
}
