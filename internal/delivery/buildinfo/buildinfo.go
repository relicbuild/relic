// Package buildinfo stores published build info and implements build
// promotion and retention.
package buildinfo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"
	"uuid"

	"relic/internal/artifacts/repository"
	"relic/internal/platform/database"
	"relic/internal/platform/telemetry"
)

// Errors mapped by the API layer.
var (
	ErrNotFound = errors.New("build not found")
	ErrInvalid  = errors.New("invalid build info")
)

// Document is what Relic reads from a published build run.
type Document struct {
	Name       string
	Number     string
	Started    time.Time
	Agent      Agent
	VCS        []VCS
	Modules    []Module
	Properties map[string]string
}

// Agent is the CI tool that published a run.
type Agent struct {
	Name    string
	Version string
}

// VCS is the source a run was built from.
type VCS struct {
	URL      string
	Revision string
	Branch   string
}

// Module is one module of a build run.
type Module struct {
	ID           string
	Type         string
	Artifacts    []Artifact
	Dependencies []Dependency
}

// Artifact is a file a module produced.
type Artifact struct {
	Name   string
	Type   string
	Path   string
	SHA1   string
	SHA256 string
	MD5    string
}

// Dependency is a package a module used.
type Dependency struct {
	ID     string
	Type   string
	Scopes []string
	SHA1   string
	SHA256 string
	MD5    string
}

// Format is a build document format.
type Format interface {
	// Parse reads a published document.
	Parse(raw []byte) (Document, error)
	// Render writes a document in the format.
	Render(doc Document) ([]byte, error)
	// Prepare returns the document as it is stored and served back.
	Prepare(
		ctx context.Context,
		raw []byte,
		sha256Of func(ctx context.Context, sha1 string) (string, bool, error),
	) ([]byte, error)
	// Path is where a run's document is stored in its build-info repository.
	Path(name, number string, started time.Time) string
}

// Service manages build info.
type Service struct {
	db     *database.DB
	repos  *repository.Service
	format Format
	layout Layout
	log    *slog.Logger
	now    func() time.Time
}

// NewService returns a Service.
func NewService(db *database.DB, repos *repository.Service, format Format, log *slog.Logger) *Service {
	return &Service{
		db:     db,
		repos:  repos,
		format: format,
		layout: DefaultLayout,
		log:    log,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

// Layout names the repositories build-info documents are stored in.
type Layout struct {
	// DefaultRepository holds builds outside projects.
	DefaultRepository string
	// ProjectSuffix names a project's repository: <project><ProjectSuffix>.
	ProjectSuffix string
}

// DefaultLayout is Relic's own.
var DefaultLayout = Layout{
	DefaultRepository: "relic-build-info",
	ProjectSuffix:     "-relic-build-info",
}

// Repository is the repository holding a project's build-info documents.
func (l Layout) Repository(project string) string {
	if project == "" || project == "default" {
		return l.DefaultRepository
	}

	return project + l.ProjectSuffix
}

// Project is the project whose build info repository holds; "" for the
// default repository or one that is not a build-info repository.
func (l Layout) Project(repository string) string {
	project, ok := strings.CutSuffix(repository, l.ProjectSuffix)
	if !ok || repository == l.DefaultRepository {
		return ""
	}

	return project
}

// SetLayout sets the build-info repository names.
func (s *Service) SetLayout(l Layout) { s.layout = l }

// Repository is the repository holding a project's build-info documents.
func (s *Service) Repository(project string) string { return s.layout.Repository(project) }

// PublishDocument stores a build run published as a Document.
func (s *Service) PublishDocument(ctx context.Context, project string, doc Document, user string) error {
	if doc.Name == "" || doc.Number == "" || doc.Started.IsZero() {
		return fmt.Errorf("%w: name, number and start are required", ErrInvalid)
	}

	raw, err := s.format.Render(doc)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}

	return s.Publish(ctx, project, raw, user)
}

// Publish stores a build document, as the format prepares it.
func (s *Service) Publish(ctx context.Context, project string, raw []byte, user string) (err error) {
	ctx, done := telemetry.Operation(ctx, "build.Publish", telemetry.BuildPublished)
	defer func() {
		done(err)
	}()

	bi, err := s.format.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}

	if bi.Name == "" || bi.Number == "" {
		return fmt.Errorf("%w: name and number are required", ErrInvalid)
	}

	started := bi.Started

	if raw, err = s.format.Prepare(ctx, raw, s.sha256Of); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}

	if err := s.ensureRepo(ctx, project); err != nil {
		return err
	}

	id := uuid.NewV7()

	var (
		art database.InsertBuildArtifactsParams
		dep database.InsertBuildDependenciesParams
	)

	for _, m := range bi.Modules {
		for _, a := range m.Artifacts {
			art.ModuleIds, art.Names, art.Types, art.Paths = append(art.ModuleIds, m.ID), append(
				art.Names,
				a.Name,
			), append(art.Types, a.Type), append(art.Paths, a.Path)
			art.Sha1s, art.Sha256s, art.Md5s = append(art.Sha1s, a.SHA1), append(art.Sha256s, a.SHA256), append(
				art.Md5s,
				a.MD5,
			)
		}

		for _, d := range m.Dependencies {
			dep.ModuleIds, dep.DepIds, dep.Types, dep.Scopes = append(dep.ModuleIds, m.ID), append(
				dep.DepIds,
				d.ID,
			), append(dep.Types, d.Type), append(dep.Scopes, strings.Join(d.Scopes, ","))
			dep.Sha1s, dep.Sha256s, dep.Md5s = append(dep.Sha1s, d.SHA1), append(dep.Sha256s, d.SHA256), append(
				dep.Md5s,
				d.MD5,
			)
		}
	}

	art.BuildID, dep.BuildID = id, id

	err = s.db.WithTx(ctx, func(q database.Querier) error {
		replaced, err := q.ListBuildRunIDsStartedAt(ctx, database.ListBuildRunIDsStartedAtParams{
			Project:   project,
			Name:      bi.Name,
			Number:    bi.Number,
			StartedMs: started.UnixMilli(),
		})
		if err != nil {
			return fmt.Errorf("find the run to replace: %w", err)
		}

		err = q.InsertBuild(ctx, database.InsertBuildParams{
			ID:        id,
			Project:   project,
			Name:      bi.Name,
			Number:    bi.Number,
			Started:   started,
			StartedMs: started.UnixMilli(),
			Info:      raw,
			CreatedBy: user,
		})
		if err != nil {
			return fmt.Errorf("insert build: %w", err)
		}

		if len(art.Names) > 0 {
			if err := q.InsertBuildArtifacts(ctx, art); err != nil {
				return fmt.Errorf("insert build artifacts: %w", err)
			}
		}

		if len(dep.DepIds) > 0 {
			if err := q.InsertBuildDependencies(ctx, dep); err != nil {
				return fmt.Errorf("insert build dependencies: %w", err)
			}
		}

		return replaceRuns(ctx, q, replaced, id)
	})
	if err != nil {
		return err
	}

	_, err = s.repos.Deploy(ctx, repository.DeployInput{
		Repository: s.Repository(project),
		Path:       s.format.Path(bi.Name, bi.Number, started),
		Body:       bytes.NewReader(raw),
		Props: repository.Properties{
			"build.name": {
				bi.Name,
			},
			"build.number": {
				bi.Number,
			},
		},
		User: user,
	})
	if err != nil {
		return errors.Join(
			fmt.Errorf("store build-info file: %w", err),
			s.db.DeleteBuilds(context.WithoutCancel(ctx), []uuid.UUID{
				id,
			}),
		)
	}

	return nil
}

// replaceRuns hands the promotions of the replaced runs to the run that
// replaces them, and deletes the replaced runs.
func replaceRuns(ctx context.Context, q database.Querier, replaced []uuid.UUID, replacement uuid.UUID) error {
	if len(replaced) == 0 {
		return nil
	}

	err := q.MoveBuildPromotions(ctx, database.MoveBuildPromotionsParams{
		ToBuildID:    replacement,
		FromBuildIds: replaced,
	})
	if err != nil {
		return fmt.Errorf("keep the promotions of the replaced run: %w", err)
	}

	if err := q.DeleteBuilds(ctx, replaced); err != nil {
		return fmt.Errorf("delete the replaced run: %w", err)
	}

	return nil
}

// sha256Of looks up stored content's SHA-256 by its SHA-1.
func (s *Service) sha256Of(ctx context.Context, sha1 string) (string, bool, error) {
	b, err := s.db.GetBlobBySHA1(ctx, sha1)
	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		return "", false, nil
	}

	if err != nil {
		return "", false, fmt.Errorf("look up %s: %w", sha1, err)
	}

	return b.Sha256, true, nil
}

func (s *Service) ensureRepo(ctx context.Context, project string) error {
	key := s.Repository(project)
	if _, err := s.repos.GetRepository(ctx, key); !errors.Is(err, repository.ErrRepositoryNotFound) {
		return err
	}

	if key != s.layout.DefaultRepository {
		_, err := s.db.GetProject(ctx, project)
		if errors.Is(database.NotFound(err), database.ErrNotFound) {
			return fmt.Errorf("%w: project %q does not exist", ErrInvalid, project)
		}

		if err != nil {
			return fmt.Errorf("get project %s: %w", project, err)
		}
	}

	r := repository.Repository{
		Name:    key,
		Class:   repository.ClassLocal,
		Format:  repository.FormatBuild,
		Project: project,
	}
	if err := s.repos.CreateRepository(ctx, r); err != nil && !errors.Is(err, repository.ErrRepositoryExists) {
		return fmt.Errorf("create %s: %w", key, err)
	}

	return nil
}

// Split resolves "name[/number]" where the name itself may contain '/'.
func (s *Service) Split(ctx context.Context, project, rest string) (name, number string, err error) {
	parts := strings.Split(strings.Trim(rest, "/"), "/")

	candidates := make([]string, len(parts))
	for i := range parts {
		candidates[i] = strings.Join(parts[:i+1], "/")
	}

	found, err := s.db.FindBuildNames(ctx, database.FindBuildNamesParams{
		Project:    project,
		Candidates: candidates,
	})
	if err != nil {
		return "", "", fmt.Errorf("find build names: %w", err)
	}

	for i, candidate := range slices.Backward(candidates) {
		if slices.Contains(found, candidate) {
			return candidate, strings.Join(parts[i+1:], "/"), nil
		}
	}

	return "", "", ErrNotFound
}

// Run is one published run of a build.
type Run struct {
	Number  string
	Started time.Time
}

// Runs lists a build's runs, newest first.
func (s *Service) Runs(ctx context.Context, project, name string) ([]Run, error) {
	rows, err := s.db.ListBuildRuns(ctx, database.ListBuildRunsParams{
		Project: project,
		Name:    name,
	})
	if err != nil {
		return nil, fmt.Errorf("list runs of %s: %w", name, err)
	}

	if len(rows) == 0 {
		return nil, ErrNotFound
	}

	runs := make([]Run, len(rows))
	for i, r := range rows {
		runs[i] = Run{
			Number:  r.Number,
			Started: r.Started,
		}
	}

	return runs, nil
}

// Summary is one build name with its newest run time.
type Summary struct {
	Name        string
	LastStarted time.Time
}

// List returns every build name in a project.
func (s *Service) List(ctx context.Context, project string) ([]Summary, error) {
	rows, err := s.db.ListBuildNames(ctx, project)
	if err != nil {
		return nil, fmt.Errorf("list builds: %w", err)
	}

	out := make([]Summary, len(rows))
	for i, r := range rows {
		out[i] = Summary{
			Name:        r.Name,
			LastStarted: r.LastStarted,
		}
	}

	return out, nil
}

// BuildRun is one published run.
type BuildRun struct {
	Project  string
	Document Document
	Raw      json.RawMessage
	Statuses []Status
}

// Status is a status a promotion set on a run.
type Status struct {
	Status     string
	Comment    string
	Repository string // the promotion's target, if any
	User       string
	Created    time.Time
}

// Run returns the newest run of name/number.
func (s *Service) Run(ctx context.Context, project, name, number string) (BuildRun, error) {
	b, err := s.run(ctx, project, name, number)
	if err != nil {
		return BuildRun{}, err
	}

	doc, err := s.format.Parse(b.Info)
	if err != nil {
		return BuildRun{}, fmt.Errorf("read stored build %s/%s: %w", name, number, err)
	}

	promos, err := s.db.ListBuildPromotions(ctx, b.ID)
	if err != nil {
		return BuildRun{}, fmt.Errorf("list promotions: %w", err)
	}

	run := BuildRun{
		Project:  b.Project,
		Document: doc,
		Raw:      b.Info,
	}
	for _, p := range promos {
		run.Statuses = append(
			run.Statuses,
			Status{
				Status:     p.Status,
				Comment:    p.Comment,
				Repository: p.Repository,
				User:       p.UserName,
				Created:    p.Created,
			},
		)
	}

	return run, nil
}

func (s *Service) run(ctx context.Context, project, name, number string) (database.Build, error) {
	b, err := s.db.GetBuildRun(ctx, database.GetBuildRunParams{
		Project: project,
		Name:    name,
		Number:  number,
	})
	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		return database.Build{}, ErrNotFound
	}

	if err != nil {
		return database.Build{}, fmt.Errorf("get build %s/%s: %w", name, number, err)
	}

	return b, nil
}
