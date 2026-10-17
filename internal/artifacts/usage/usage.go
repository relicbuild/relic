// Package usage records and reports how repositories are used: daily
// storage snapshots, downloads and deploys, by repository, project and user.
package usage

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"relic/internal/artifacts/repository"
	"relic/internal/platform/database"
	"relic/internal/platform/events"
	"relic/internal/platform/telemetry"
)

// Retention is how long daily usage is kept.
const Retention = 2 * 365 * 24 * time.Hour

const dayFormat = time.DateOnly

func day(t time.Time) string { return t.UTC().Format(dayFormat) }

type tally struct {
	downloads, deploys int64
}

type repoDay struct {
	day, repository string
}

type userDay struct {
	day, user string
}

// Recorder counts downloads and deploys in memory and writes them on Flush.
// It implements events.Publisher for deploys.
type Recorder struct {
	db  *database.DB
	log *slog.Logger
	now func() time.Time

	mu    sync.Mutex
	repos map[repoDay]tally
	users map[userDay]tally
}

// NewRecorder returns a Recorder writing to db.
func NewRecorder(db *database.DB, log *slog.Logger) *Recorder {
	return &Recorder{
		db:    db,
		log:   log,
		now:   time.Now,
		repos: map[repoDay]tally{},
		users: map[userDay]tally{},
	}
}

func (r *Recorder) add(repo, user string, downloads, deploys int64) {
	if r == nil {
		return
	}

	d := day(r.now())

	r.mu.Lock()
	defer r.mu.Unlock()

	rk := repoDay{
		day:        d,
		repository: repo,
	}
	t := r.repos[rk]
	t.downloads += downloads
	t.deploys += deploys
	r.repos[rk] = t

	if user == "" {
		return
	}

	uk := userDay{
		day:  d,
		user: user,
	}
	u := r.users[uk]
	u.downloads += downloads
	u.deploys += deploys
	r.users[uk] = u
}

// Download counts a download from repo by user.
func (r *Recorder) Download(repo, user string) { r.add(repo, user, 1, 0) }

// Publish implements events.Publisher: it counts deploys.
func (r *Recorder) Publish(_ context.Context, e events.Event) {
	if e.Type != events.ArtifactDeployed || e.Actor == repository.SystemUser {
		return
	}

	repo, _ := e.Data["repository"].(string)
	r.add(repo, e.Actor, 0, 1)
}

// Flush writes the counts gathered so far; counts it could not write are
// kept for the next flush.
func (r *Recorder) Flush(ctx context.Context) error {
	if r == nil {
		return nil
	}

	r.mu.Lock()
	repos, users := r.repos, r.users
	r.repos, r.users = map[repoDay]tally{}, map[userDay]tally{}
	r.mu.Unlock()

	var errs []error

	for k, t := range repos {
		if err := r.db.AddRepositoryUsage(ctx, database.AddRepositoryUsageParams{
			Day:       k.day,
			RepoKey:   k.repository,
			Downloads: t.downloads,
			Deploys:   t.deploys,
		}); err != nil {
			errs = append(errs, fmt.Errorf("record repository usage: %w", err))

			continue
		}

		delete(repos, k)
	}

	for k, t := range users {
		if err := r.db.AddUserUsage(ctx, database.AddUserUsageParams{
			Day:       k.day,
			Username:  k.user,
			Downloads: t.downloads,
			Deploys:   t.deploys,
		}); err != nil {
			errs = append(errs, fmt.Errorf("record user usage: %w", err))

			continue
		}

		delete(users, k)
	}

	r.keep(repos, users)

	return errors.Join(errs...)
}

func (r *Recorder) keep(repos map[repoDay]tally, users map[userDay]tally) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for k, t := range repos {
		kept := r.repos[k]
		kept.downloads += t.downloads
		kept.deploys += t.deploys
		r.repos[k] = kept
	}

	for k, t := range users {
		kept := r.users[k]
		kept.downloads += t.downloads
		kept.deploys += t.deploys
		r.users[k] = kept
	}
}

// Run flushes every interval until ctx ends, then once more.
func (r *Recorder) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			if err := r.Flush(context.WithoutCancel(ctx)); err != nil {
				r.log.WarnContext(ctx, "flush usage", "error", err)
			}

			return
		case <-t.C:
			if err := r.Flush(ctx); err != nil {
				r.log.WarnContext(ctx, "flush usage", "error", err)
			}
		}
	}
}

// Service snapshots and reports usage.
type Service struct {
	db       *database.DB
	projects func(context.Context) (map[string]string, error)
	now      func() time.Time
}

// NewService returns a Service; projects maps repositories to projects.
func NewService(db *database.DB, projects func(context.Context) (map[string]string, error)) *Service {
	return &Service{
		db:       db,
		projects: projects,
		now:      time.Now,
	}
}

// Snapshot records every repository's files and bytes for today, and drops
// usage older than Retention.
func (s *Service) Snapshot(ctx context.Context) (err error) {
	ctx, span := telemetry.Tracer().Start(ctx, "usage.Snapshot")
	defer func() {
		telemetry.End(span, err)
	}()

	now := s.now()
	if err := s.db.SnapshotRepositoryUsage(ctx, day(now)); err != nil {
		return fmt.Errorf("snapshot usage: %w", err)
	}

	before := day(now.Add(-Retention))
	if err := s.db.PurgeUsage(ctx, before); err != nil {
		return fmt.Errorf("purge usage: %w", err)
	}

	if err := s.db.PurgeUserUsage(ctx, before); err != nil {
		return fmt.Errorf("purge user usage: %w", err)
	}

	return nil
}

// RepositoryUsage is one repository over a period.
type RepositoryUsage struct {
	Repository string
	Project    string
	// Files and Bytes are the latest snapshot's; Growth is the bytes since
	// the period's first snapshot.
	Files, Bytes, Growth int64
	Downloads, Deploys   int64
}

// ProjectUsage sums a project's repositories.
type ProjectUsage struct {
	Project            string
	Bytes, Growth      int64
	Downloads, Deploys int64
}

// DayUsage is one day's totals.
type DayUsage struct {
	Day                string
	Bytes              int64
	Downloads, Deploys int64
}

// UserUsage is one user's downloads and deploys over a period.
type UserUsage struct {
	User               string
	Downloads, Deploys int64
}

// Report is usage over a period.
type Report struct {
	From, To       string
	Repositories   []RepositoryUsage
	Projects       []ProjectUsage
	Days           []DayUsage
	TopDownloaders []UserUsage
	TopDeployers   []UserUsage
}

// TopUsers is how many users the report ranks.
const TopUsers = 10

// Report returns usage over the last days days, today included.
func (s *Service) Report(ctx context.Context, days int) (_ Report, err error) {
	ctx, span := telemetry.Tracer().Start(ctx, "usage.Report")
	defer func() {
		telemetry.End(span, err)
	}()

	to := s.now()
	from := to.AddDate(0, 0, -(max(days, 1) - 1))

	out := Report{
		From: day(from),
		To:   day(to),
	}

	rows, err := s.db.ListRepositoryUsage(ctx, database.ListRepositoryUsageParams{
		FromDay: out.From,
		ToDay:   out.To,
	})
	if err != nil {
		return Report{}, fmt.Errorf("list usage: %w", err)
	}

	projects := map[string]string{}
	if s.projects != nil {
		if projects, err = s.projects(ctx); err != nil {
			return Report{}, err
		}
	}

	type period struct {
		first, latest int64
		firstDay      string
		latestDay     string
		repo          RepositoryUsage
	}

	byRepo := map[string]*period{}
	byDay := map[string]*DayUsage{}

	for _, r := range rows {
		sp := byRepo[r.RepoKey]
		if sp == nil {
			sp = &period{
				repo: RepositoryUsage{
					Repository: r.RepoKey,
					Project:    projects[r.RepoKey],
				},
			}
			byRepo[r.RepoKey] = sp
		}

		sp.repo.Downloads += r.Downloads
		sp.repo.Deploys += r.Deploys

		if r.Files > 0 || r.Bytes > 0 {
			if sp.firstDay == "" || r.Day < sp.firstDay {
				sp.firstDay, sp.first = r.Day, r.Bytes
			}

			if r.Day >= sp.latestDay {
				sp.latestDay, sp.latest = r.Day, r.Bytes
				sp.repo.Files, sp.repo.Bytes = r.Files, r.Bytes
			}
		}

		d := byDay[r.Day]
		if d == nil {
			d = &DayUsage{
				Day: r.Day,
			}
			byDay[r.Day] = d
		}

		d.Bytes += r.Bytes
		d.Downloads += r.Downloads
		d.Deploys += r.Deploys
	}

	byProject := map[string]*ProjectUsage{}

	for _, sp := range byRepo {
		sp.repo.Growth = sp.latest - sp.first
		out.Repositories = append(out.Repositories, sp.repo)

		if sp.repo.Project == "" {
			continue
		}

		p := byProject[sp.repo.Project]
		if p == nil {
			p = &ProjectUsage{
				Project: sp.repo.Project,
			}
			byProject[sp.repo.Project] = p
		}

		p.Bytes += sp.repo.Bytes
		p.Growth += sp.repo.Growth
		p.Downloads += sp.repo.Downloads
		p.Deploys += sp.repo.Deploys
	}

	slices.SortFunc(out.Repositories, func(a, b RepositoryUsage) int {
		return cmp.Or(cmp.Compare(b.Bytes, a.Bytes), cmp.Compare(a.Repository, b.Repository))
	})

	for _, p := range byProject {
		out.Projects = append(out.Projects, *p)
	}

	slices.SortFunc(out.Projects, func(a, b ProjectUsage) int {
		return cmp.Or(cmp.Compare(b.Bytes, a.Bytes), cmp.Compare(a.Project, b.Project))
	})

	for _, d := range byDay {
		out.Days = append(out.Days, *d)
	}

	slices.SortFunc(out.Days, func(a, b DayUsage) int {
		return cmp.Compare(a.Day, b.Day)
	})

	users, err := s.db.ListUserUsage(ctx, database.ListUserUsageParams{
		FromDay: out.From,
		ToDay:   out.To,
	})
	if err != nil {
		return Report{}, fmt.Errorf("list user usage: %w", err)
	}

	out.TopDownloaders = topUsers(users, func(u database.ListUserUsageRow) int64 {
		return u.Downloads
	})
	out.TopDeployers = topUsers(users, func(u database.ListUserUsageRow) int64 {
		return u.Deploys
	})

	return out, nil
}

func topUsers(rows []database.ListUserUsageRow, count func(database.ListUserUsageRow) int64) []UserUsage {
	ranked := slices.Clone(rows)
	ranked = slices.DeleteFunc(ranked, func(u database.ListUserUsageRow) bool {
		return count(u) == 0
	})

	slices.SortFunc(ranked, func(a, b database.ListUserUsageRow) int {
		return cmp.Or(cmp.Compare(count(b), count(a)), cmp.Compare(a.Username, b.Username))
	})

	out := make([]UserUsage, 0, min(len(ranked), TopUsers))
	for _, u := range ranked[:min(len(ranked), TopUsers)] {
		out = append(out, UserUsage{
			User:      u.Username,
			Downloads: u.Downloads,
			Deploys:   u.Deploys,
		})
	}

	return out
}
