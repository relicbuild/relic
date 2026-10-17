package buildinfo

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
	"uuid"

	"relic/internal/access/auth"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/database"
	"relic/internal/platform/telemetry"
)

// Promotion copies or moves a run's files to a target repository and
// records a status on the run.
type Promotion struct {
	Status string
	// Comment is recorded with the status.
	Comment string
	// SourceRepository limits the files to one repository; empty for all.
	SourceRepository string
	// TargetRepository receives the files; empty to record a status only.
	TargetRepository string
	// Dependencies promotes the run's dependencies too.
	Dependencies bool
	// Copy keeps the files where they are; otherwise they move.
	Copy bool
	// FailFast stops at the first file that fails.
	FailFast bool
	// DryRun reports what would happen and changes nothing.
	DryRun bool
	// Properties are set on the promoted files.
	Properties map[string][]string
}

// ItemCheck reports whether the caller may perform a on a stored item.
type ItemCheck func(item repository.Item, a auth.Action) bool

// AllowAll is an ItemCheck for callers already authorized for everything.
func AllowAll(repository.Item, auth.Action) bool { return true }

// Level is a report line's severity.
type Level string

// Report levels.
const (
	LevelWarning Level = "warning"
	LevelError   Level = "error"
)

// Message is one line of a promotion report.
type Message struct {
	Level Level
	Text  string
}

// Promote copies or moves a build's artifacts (and optionally dependencies)
// to the target repository, sets properties on them, and records the status.
func (s *Service) Promote(
	ctx context.Context,
	project, name, number string,
	req Promotion,
	user string,
	check ItemCheck,
) (_ []Message, err error) {
	ctx, done := telemetry.Operation(ctx, "build.Promote", func(err error) {
		telemetry.Promoted("build", err)
	})
	defer func() {
		done(err)
	}()

	b, err := s.run(ctx, project, name, number)
	if err != nil {
		return nil, err
	}

	if req.TargetRepository == "" && req.Status == "" {
		return nil, fmt.Errorf("%w: targetRepo or status is required", ErrInvalid)
	}

	items, err := s.buildItems(ctx, b, itemQuery{
		sourceRepository: req.SourceRepository,
		dependencies:     req.Dependencies,
	})
	if err != nil {
		return nil, err
	}

	var msgs []Message

	if req.TargetRepository != "" {
		if _, err := s.repos.GetRepository(ctx, req.TargetRepository); err != nil {
			return nil, err
		}

		for _, it := range items {
			if it.Repository == req.TargetRepository {
				continue
			}

			target := repository.Item{
				Repository: req.TargetRepository,
				Parent:     it.Parent,
				Name:       it.Name,
			}
			if !check(it, auth.ActionRead) || (!req.Copy && !check(it, auth.ActionDelete)) ||
				!check(target, auth.ActionWrite) {
				msgs = append(
					msgs,
					Message{
						Level: LevelWarning,
						Text:  fmt.Sprintf("skipped %s/%s: permission denied", it.Repository, it.Path()),
					},
				)

				continue
			}

			err := s.repos.Copy(
				ctx,
				repository.CopyInput{
					Source:                it,
					DestinationRepository: req.TargetRepository,
					DestinationPath:       it.Path(),
					Move:                  !req.Copy,
					DryRun:                req.DryRun,
					User:                  user,
				},
			)
			if err == nil && !req.DryRun && len(req.Properties) > 0 {
				var promoted repository.Item
				if promoted, err = s.repos.Stat(ctx, req.TargetRepository, it.Path()); err == nil {
					err = s.repos.SetProperties(ctx, promoted, repository.Properties(req.Properties), false)
				}
			}

			if err != nil {
				msgs = append(
					msgs,
					Message{
						Level: LevelError,
						Text:  fmt.Sprintf("failed to promote %s/%s: %v", it.Repository, it.Path(), err),
					},
				)
				if req.FailFast {
					return msgs, fmt.Errorf("promote %s/%s: %w", it.Repository, it.Path(), err)
				}
			}
		}
	}

	if req.DryRun {
		return msgs, nil
	}

	status := req.Status
	if status == "" {
		status = "promoted"
	}

	err = s.db.InsertBuildPromotion(ctx, database.InsertBuildPromotionParams{
		ID:         uuid.NewV7(),
		BuildID:    b.ID,
		Status:     status,
		Comment:    req.Comment,
		Repository: req.TargetRepository,
		UserName:   user,
		Created:    s.now(),
	})
	if err != nil {
		return msgs, fmt.Errorf("record promotion: %w", err)
	}

	return msgs, nil
}

// itemQuery selects the stored files of a build run.
type itemQuery struct {
	// sourceRepository limits the files to one repository; empty for all.
	sourceRepository string
	// dependencies adds the run's dependencies to its artifacts.
	dependencies bool
	// recordedOnly leaves out files that only share a recorded file's content.
	recordedOnly bool
}

// recordedFile is a file a build run lists: its content and, for an
// artifact, the path it was deployed to.
type recordedFile struct {
	sha1 string
	path string
}

// match says how surely a stored file is one a build run recorded.
type match int

const (
	matchContent match = iota
	matchBuildName
	matchPath
	matchBuildRun
)

func (s *Service) buildItems(ctx context.Context, b database.Build, query itemQuery) ([]repository.Item, error) {
	arts, err := s.db.ListBuildArtifacts(ctx, b.ID)
	if err != nil {
		return nil, fmt.Errorf("list build artifacts: %w", err)
	}

	files := make([]recordedFile, 0, len(arts))
	for _, a := range arts {
		files = append(files, recordedFile{
			sha1: a.Sha1,
			path: a.Path,
		})
	}

	items, err := s.recordedItems(ctx, b, files, query, true)
	if err != nil || !query.dependencies {
		return items, err
	}

	deps, err := s.db.ListBuildDependencies(ctx, b.ID)
	if err != nil {
		return nil, fmt.Errorf("list build dependencies: %w", err)
	}

	depFiles := make([]recordedFile, 0, len(deps))
	for _, d := range deps {
		depFiles = append(depFiles, recordedFile{
			sha1: d.Sha1,
		})
	}

	depItems, err := s.recordedItems(ctx, b, depFiles, query, false)

	return append(items, depItems...), err
}

func (s *Service) recordedItems(
	ctx context.Context,
	b database.Build,
	files []recordedFile,
	query itemQuery,
	all bool,
) ([]repository.Item, error) {
	if len(files) == 0 {
		return nil, nil
	}

	sha1s := make([]string, 0, len(files))
	paths := map[string][]string{}

	for _, f := range files {
		sha1s = append(sha1s, f.sha1)
		if f.path != "" {
			paths[f.sha1] = append(paths[f.sha1], f.path)
		}
	}

	rows, err := s.db.FindItemsBySHA1(
		ctx,
		database.FindItemsBySHA1Params{
			BuildName:   b.Name,
			BuildNumber: b.Number,
			Sha1s:       sha1s,
			SourceRepo:  query.sourceRepository,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("find build items: %w", err)
	}

	type ranked struct {
		match match
		items []database.Item
	}

	best := map[string]*ranked{}

	var order []string

	for _, r := range rows {
		sha := *r.Item.Sha1
		m := matchContent

		switch {
		case r.HasBuildName && r.HasBuildNumber:
			m = matchBuildRun
		case recordedAt(paths[sha], r.Item):
			m = matchPath
		case r.HasBuildName:
			m = matchBuildName
		}

		if query.recordedOnly && m == matchContent {
			continue
		}

		t, ok := best[sha]
		switch {
		case !ok:
			best[sha] = &ranked{
				match: m,
				items: []database.Item{
					r.Item,
				},
			}
			order = append(order, sha)
		case m > t.match:
			t.match, t.items = m, []database.Item{
				r.Item,
			}
		case m == t.match && all && m > matchContent:
			t.items = append(t.items, r.Item)
		}
	}

	var out []repository.Item

	for _, sha := range order {
		for _, it := range best[sha].items {
			stat, err := s.repos.Stat(ctx, it.RepoKey, repository.JoinPath(it.Path, it.Name))
			if err != nil {
				return nil, err
			}

			out = append(out, stat)
		}
	}

	return out, nil
}

// recordedAt reports whether it is at one of the recorded paths, which name
// the file inside its repository or start with the repository.
func recordedAt(paths []string, it database.Item) bool {
	full := repository.JoinPath(it.Path, it.Name)

	return slices.ContainsFunc(paths, func(p string) bool {
		return p == full || p == it.RepoKey+"/"+full
	})
}

// Artifacts returns the stored files of a build's artifacts.
func (s *Service) Artifacts(
	ctx context.Context,
	project, name, number string,
	check ItemCheck,
) ([]repository.Item, error) {
	b, err := s.run(ctx, project, name, number)
	if err != nil {
		return nil, err
	}

	items, err := s.buildItems(ctx, b, itemQuery{})
	if err != nil {
		return nil, err
	}

	var out []repository.Item

	for _, it := range items {
		if check(it, auth.ActionRead) {
			out = append(out, it)
		}
	}

	return out, nil
}

// Retention says which runs of a build to keep.
type Retention struct {
	// MaxRuns keeps at most this many runs, newest first; 0 keeps any number.
	MaxRuns int
	// MinStarted discards runs started before it; zero keeps runs of any age.
	MinStarted time.Time
	// Keep lists run numbers that are never discarded.
	Keep []string
	// DeleteArtifacts deletes the discarded runs' artifacts too.
	DeleteArtifacts bool
}

// Discard applies a retention policy to a build's runs.
func (s *Service) Discard(ctx context.Context, project, name string, req Retention, check ItemCheck) error {
	if req.MaxRuns < 0 {
		return fmt.Errorf("%w: the maximum number of runs cannot be negative", ErrInvalid)
	}

	runs, err := s.db.ListBuildRuns(ctx, database.ListBuildRunsParams{
		Project: project,
		Name:    name,
	})
	if err != nil {
		return fmt.Errorf("list runs of %s: %w", name, err)
	}

	var doomed []database.ListBuildRunsRow

	kept := 0

	for _, r := range runs {
		switch {
		case contains(req.Keep, r.Number):
		case req.MaxRuns > 0 && kept >= req.MaxRuns, !req.MinStarted.IsZero() && r.Started.Before(req.MinStarted):
			doomed = append(doomed, r)
			continue
		}

		kept++
	}

	return s.deleteRuns(ctx, project, name, doomed, req.DeleteArtifacts, check)
}

// Delete removes the given build numbers, or every run when all is set.
func (s *Service) Delete(
	ctx context.Context,
	project, name string,
	numbers []string,
	all, deleteArtifacts bool,
	check ItemCheck,
) error {
	runs, err := s.db.ListBuildRuns(ctx, database.ListBuildRunsParams{
		Project: project,
		Name:    name,
	})
	if err != nil {
		return fmt.Errorf("list runs of %s: %w", name, err)
	}

	var doomed []database.ListBuildRunsRow

	for _, r := range runs {
		if all || contains(numbers, r.Number) {
			doomed = append(doomed, r)
		}
	}

	if len(doomed) == 0 {
		return ErrNotFound
	}

	return s.deleteRuns(ctx, project, name, doomed, deleteArtifacts, check)
}

func (s *Service) deleteRuns(
	ctx context.Context,
	project, name string,
	runs []database.ListBuildRunsRow,
	deleteArtifacts bool,
	check ItemCheck,
) error {
	ids := make([]uuid.UUID, 0, len(runs))
	for _, r := range runs {
		if err := s.deleteRunFiles(ctx, project, name, r, deleteArtifacts, check); err != nil {
			return errors.Join(err, s.deleteBuildRows(ctx, ids))
		}

		ids = append(ids, r.ID)
	}

	return s.deleteBuildRows(ctx, ids)
}

func (s *Service) deleteRunFiles(
	ctx context.Context,
	project, name string,
	r database.ListBuildRunsRow,
	deleteArtifacts bool,
	check ItemCheck,
) error {
	if deleteArtifacts {
		b, err := s.run(ctx, project, name, r.Number)
		if err != nil {
			return err
		}

		items, err := s.buildItems(ctx, b, itemQuery{
			recordedOnly: true,
		})
		if err != nil {
			return err
		}

		for _, it := range items {
			if !check(it, auth.ActionDelete) {
				s.log.InfoContext(
					ctx,
					"build artifact kept: no delete permission",
					"build",
					name,
					"repo",
					it.Repository,
					"path",
					it.Path(),
				)

				continue
			}

			if err := s.repos.Delete(ctx, it.Repository, it.Path()); err != nil &&
				!errors.Is(err, repository.ErrNotFound) {
				return err
			}
		}
	}

	err := s.repos.Delete(ctx, s.Repository(project), s.format.Path(name, r.Number, r.Started))
	if err != nil && !errors.Is(err, repository.ErrNotFound) && !errors.Is(err, repository.ErrRepositoryNotFound) {
		return fmt.Errorf("delete build-info file: %w", err)
	}

	return nil
}

func (s *Service) deleteBuildRows(ctx context.Context, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}

	if err := s.db.DeleteBuilds(ctx, ids); err != nil {
		return fmt.Errorf("delete builds: %w", err)
	}

	return nil
}

func contains(list []string, v string) bool {
	return slices.Contains(list, v)
}

// Items returns the stored files for a build run's artifacts and, with
// withDeps, its dependencies.
func (s *Service) Items(ctx context.Context, project, name, number string, withDeps bool) ([]repository.Item, error) {
	b, err := s.run(ctx, project, name, number)
	if err != nil {
		return nil, err
	}

	return s.buildItems(ctx, b, itemQuery{
		dependencies: withDeps,
	})
}
