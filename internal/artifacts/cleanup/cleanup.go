// Package cleanup moves what cleanup policies select into the trash.
package cleanup

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"relic/internal/access/auth"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/database"
	"relic/internal/platform/telemetry"
)

// Errors of the cleanup service.
var (
	ErrNotFound = errors.New("cleanup policy not found")
	ErrInvalid  = errors.New("invalid cleanup policy")
)

// Policy selects items to move to the trash: those matching PathPattern in
// Repositories, created more than OlderThanDays ago and not downloaded for
// NotDownloadedDays, except the KeepLatest newest of each folder. Files Relic
// writes itself, such as maven-metadata.xml, and held items are never
// selected.
type Policy struct {
	Name              string    `json:"-"`
	Enabled           bool      `json:"enabled"`
	Repositories      []string  `json:"repositories"`
	PathPattern       string    `json:"pathPattern"`
	OlderThanDays     int       `json:"olderThanDays,omitempty"`
	NotDownloadedDays int       `json:"notDownloadedDays,omitempty"`
	KeepLatest        int       `json:"keepLatest,omitempty"`
	Created           time.Time `json:"-"`
	Updated           time.Time `json:"-"`
	// LastRun, LastMoved and LastError describe the last run.
	LastRun   *time.Time `json:"-"`
	LastMoved int64      `json:"-"`
	LastError string     `json:"-"`
}

// Candidate is an item a policy selects.
type Candidate struct {
	Repository     string
	Path           string
	Size           int64
	Created        time.Time
	LastDownloaded *time.Time
}

// Service stores cleanup policies and runs them.
type Service struct {
	db    *database.DB
	repos *repository.Service
	log   *slog.Logger
	now   func() time.Time
}

// New returns a Service. Deleting a repository removes it from the
// policies; a policy left without repositories is disabled.
func New(db *database.DB, repos *repository.Service, log *slog.Logger) *Service {
	s := &Service{
		db:    db,
		repos: repos,
		log:   log,
		now:   time.Now,
	}
	repos.RegisterRemovalHook(s.removeRepository)

	return s
}

func (s *Service) removeRepository(ctx context.Context, q database.Querier, key string) error {
	rows, err := q.ListCleanupPolicies(ctx)
	if err != nil {
		return fmt.Errorf("list cleanup policies: %w", err)
	}

	for _, row := range rows {
		p, err := policyOf(row)
		if err != nil {
			return err
		}

		kept := slices.DeleteFunc(slices.Clone(p.Repositories), func(name string) bool {
			return name == key
		})
		if len(kept) == len(p.Repositories) {
			continue
		}

		p.Repositories = kept
		p.Enabled = p.Enabled && len(kept) > 0

		doc, err := json.Marshal(p)
		if err != nil {
			return fmt.Errorf("encode cleanup policy: %w", err)
		}

		if err := q.UpsertCleanupPolicy(ctx, database.UpsertCleanupPolicyParams{
			Name: p.Name,
			Doc:  doc,
			Now:  s.now(),
		}); err != nil {
			return fmt.Errorf("store cleanup policy %s: %w", p.Name, err)
		}
	}

	return nil
}

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func (s *Service) validate(ctx context.Context, p Policy) error {
	if !namePattern.MatchString(p.Name) {
		return fmt.Errorf("%w: name %q: letters, digits, dot, dash and underscore, up to 64", ErrInvalid, p.Name)
	}

	if len(p.Repositories) == 0 && p.Enabled {
		return fmt.Errorf("%w: an enabled policy names at least one repository", ErrInvalid)
	}

	return s.validateSelection(ctx, p)
}

func (s *Service) validateSelection(ctx context.Context, p Policy) error {
	switch {
	case p.OlderThanDays < 0 || p.NotDownloadedDays < 0 || p.KeepLatest < 0:
		return fmt.Errorf("%w: days and counts cannot be negative", ErrInvalid)
	case p.OlderThanDays == 0 && p.NotDownloadedDays == 0 && p.KeepLatest == 0:
		return fmt.Errorf("%w: set olderThanDays, notDownloadedDays or keepLatest", ErrInvalid)
	}

	for _, name := range p.Repositories {
		r, err := s.repos.GetRepository(ctx, name)
		if errors.Is(err, repository.ErrRepositoryNotFound) {
			return fmt.Errorf("%w: repository %s does not exist", ErrInvalid, name)
		}

		if err != nil {
			return fmt.Errorf("repository %s: %w", name, err)
		}

		if r.Class != repository.ClassLocal || name == s.repos.TrashRepository() {
			return fmt.Errorf("%w: %s is not a local repository", ErrInvalid, name)
		}
	}

	return nil
}

func policyOf(row database.CleanupPolicy) (Policy, error) {
	var p Policy
	if err := json.Unmarshal(row.Doc, &p); err != nil {
		return Policy{}, fmt.Errorf("cleanup policy %s: %w", row.Name, err)
	}

	p.Name, p.Created, p.Updated = row.Name, row.Created, row.Updated
	p.LastRun, p.LastMoved, p.LastError = row.LastRun, row.LastMoved, row.LastError

	return p, nil
}

// List returns every policy by name.
func (s *Service) List(ctx context.Context) ([]Policy, error) {
	rows, err := s.db.ListCleanupPolicies(ctx)
	if err != nil {
		return nil, fmt.Errorf("list cleanup policies: %w", err)
	}

	out := make([]Policy, 0, len(rows))

	for _, r := range rows {
		p, err := policyOf(r)
		if err != nil {
			return nil, err
		}

		out = append(out, p)
	}

	return out, nil
}

// Get returns one policy.
func (s *Service) Get(ctx context.Context, name string) (Policy, error) {
	row, err := s.db.GetCleanupPolicy(ctx, name)
	if database.IsNoRows(err) {
		return Policy{}, fmt.Errorf("%w: %s", ErrNotFound, name)
	}

	if err != nil {
		return Policy{}, fmt.Errorf("cleanup policy %s: %w", name, err)
	}

	return policyOf(row)
}

// Put creates or replaces a policy.
func (s *Service) Put(ctx context.Context, p Policy) error {
	p.PathPattern = cmp.Or(strings.Trim(p.PathPattern, "/"), "**")

	if err := s.validate(ctx, p); err != nil {
		return err
	}

	doc, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("encode cleanup policy: %w", err)
	}

	if err := s.db.UpsertCleanupPolicy(ctx, database.UpsertCleanupPolicyParams{
		Name: p.Name,
		Doc:  doc,
		Now:  s.now(),
	}); err != nil {
		return fmt.Errorf("store cleanup policy %s: %w", p.Name, err)
	}

	return nil
}

// Delete removes a policy.
func (s *Service) Delete(ctx context.Context, name string) error {
	n, err := s.db.DeleteCleanupPolicy(ctx, name)
	if err != nil {
		return fmt.Errorf("delete cleanup policy %s: %w", name, err)
	}

	if n == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	}

	return nil
}

// Preview returns what p, a policy that need not be stored, selects now,
// without moving anything.
func (s *Service) Preview(ctx context.Context, p Policy) ([]Candidate, error) {
	if len(p.Repositories) == 0 {
		return nil, fmt.Errorf("%w: name at least one repository", ErrInvalid)
	}

	if err := s.validateSelection(ctx, p); err != nil {
		return nil, err
	}

	return s.candidates(ctx, p)
}

// PreviewByName returns what the stored policy name selects now, without
// moving anything.
func (s *Service) PreviewByName(ctx context.Context, name string) ([]Candidate, error) {
	p, err := s.Get(ctx, name)
	if err != nil {
		return nil, err
	}

	return s.candidates(ctx, p)
}

func (s *Service) candidates(ctx context.Context, p Policy) ([]Candidate, error) {
	p.PathPattern = cmp.Or(strings.Trim(p.PathPattern, "/"), "**")

	holds, err := s.repos.Holds(ctx)
	if err != nil {
		return nil, err
	}

	var out []Candidate

	for _, name := range p.Repositories {
		found, err := s.selectIn(ctx, p, name)
		if err != nil {
			return nil, err
		}

		for _, c := range found {
			if !held(holds, c) {
				out = append(out, c)
			}
		}
	}

	return out, nil
}

// held reports whether a hold covers c: on it, above it (the repository's
// root included) or under it.
func held(holds []repository.Hold, c Candidate) bool {
	return slices.ContainsFunc(holds, func(h repository.Hold) bool {
		return h.Repository == c.Repository &&
			(h.Path == "" || h.Path == c.Path || strings.HasPrefix(c.Path, h.Path+"/") ||
				strings.HasPrefix(h.Path, c.Path+"/"))
	})
}

// Run moves what the named policy selects to the trash and records the
// run; it returns how many items it moved.
func (s *Service) Run(ctx context.Context, name string) (moved int, err error) {
	ctx, done := telemetry.Operation(ctx, "cleanup.Run", telemetry.CleanupRun)

	defer func() {
		telemetry.CleanupMoved(moved)
		done(err)
	}()

	p, err := s.Get(ctx, name)
	if err != nil {
		return 0, err
	}

	moved, err = s.run(ctx, p)

	msg := ""
	if err != nil {
		msg = err.Error()
	}

	now := s.now()
	if recErr := s.db.RecordCleanupRun(ctx, database.RecordCleanupRunParams{
		Name:      name,
		LastRun:   &now,
		LastMoved: int64(moved),
		LastError: msg,
	}); recErr != nil {
		err = errors.Join(err, fmt.Errorf("record cleanup run %s: %w", name, recErr))
	}

	return moved, err
}

func (s *Service) run(ctx context.Context, p Policy) (int, error) {
	found, err := s.candidates(ctx, p)
	if err != nil {
		return 0, err
	}

	moved := 0

	for _, c := range found {
		err := s.repos.DeleteToTrash(ctx, c.Repository, c.Path, "cleanup:"+p.Name)
		if errors.Is(err, repository.ErrNotFound) {
			continue
		}

		if err != nil {
			return moved, fmt.Errorf("move %s/%s to the trash: %w", c.Repository, c.Path, err)
		}

		moved++
	}

	if moved > 0 {
		s.log.InfoContext(ctx, "cleanup policy moved items to the trash", "policy", p.Name, "count", moved)
	}

	return moved, nil
}

// RunAll runs every enabled policy and returns how many items they moved.
func (s *Service) RunAll(ctx context.Context) (int, error) {
	policies, err := s.List(ctx)
	if err != nil {
		return 0, err
	}

	var (
		total  int
		failed []error
	)

	for _, p := range policies {
		if !p.Enabled {
			continue
		}

		n, err := s.Run(ctx, p.Name)
		total += n

		if err != nil {
			failed = append(failed, fmt.Errorf("cleanup policy %s: %w", p.Name, err))
		}
	}

	return total, errors.Join(failed...)
}

// literalPrefix is the folder a pattern's matches all lie under.
func literalPrefix(pattern string) string {
	var segs []string

	for seg := range strings.SplitSeq(pattern, "/") {
		if strings.ContainsAny(seg, "*?") {
			break
		}

		segs = append(segs, seg)
	}

	if len(segs) == len(strings.Split(pattern, "/")) && len(segs) > 0 {
		segs = segs[:len(segs)-1]
	}

	return strings.Join(segs, "/")
}

// selectIn is candidates for one repository.
func (s *Service) selectIn(ctx context.Context, p Policy, repoKey string) ([]Candidate, error) {
	root, err := s.repos.Stat(ctx, repoKey, literalPrefix(p.PathPattern))
	if errors.Is(err, repository.ErrNotFound) {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	items, err := s.repos.Subtree(ctx, root)
	if err != nil {
		return nil, err
	}

	downloads, err := s.db.ListRepositoryDownloads(ctx, repoKey)
	if err != nil {
		return nil, fmt.Errorf("downloads of %s: %w", repoKey, err)
	}

	lastDownloaded := map[string]time.Time{}

	for _, d := range downloads {
		for dir := repository.JoinPath(d.Path, d.Name); dir != "." && dir != ""; dir = path.Dir(dir) {
			if d.LastDownloaded.After(lastDownloaded[dir]) {
				lastDownloaded[dir] = d.LastDownloaded
			}
		}
	}

	sizes := map[string]int64{}

	for _, it := range items {
		if it.IsFolder() {
			continue
		}

		for dir := it.Path(); dir != "." && dir != ""; dir = path.Dir(dir) {
			sizes[dir] += it.Size
		}
	}

	matched := map[string]bool{}

	var top []repository.Item

	slices.SortFunc(items, func(a, b repository.Item) int {
		return strings.Compare(a.Path(), b.Path())
	})

	for _, it := range items {
		full := it.Path()
		if it.Name == repository.RootPath || it.ModifiedBy == repository.SystemUser ||
			!auth.MatchesAntPattern(p.PathPattern, full) {
			continue
		}

		matched[full] = true

		if !matchedAbove(matched, full) {
			top = append(top, it)
		}
	}

	byFolder := map[string][]repository.Item{}
	for _, it := range top {
		byFolder[it.Parent] = append(byFolder[it.Parent], it)
	}

	now := s.now()

	var out []Candidate

	for _, group := range byFolder {
		slices.SortFunc(group, func(a, b repository.Item) int {
			return b.Created.Compare(a.Created)
		})

		for i, it := range group {
			if i < p.KeepLatest {
				continue
			}

			if p.OlderThanDays > 0 && it.Created.After(now.AddDate(0, 0, -p.OlderThanDays)) {
				continue
			}

			last, downloaded := lastDownloaded[it.Path()]
			if p.NotDownloadedDays > 0 {
				seen := it.Created
				if downloaded {
					seen = last
				}

				if seen.After(now.AddDate(0, 0, -p.NotDownloadedDays)) {
					continue
				}
			}

			c := Candidate{
				Repository: repoKey,
				Path:       it.Path(),
				Size:       sizes[it.Path()],
				Created:    it.Created,
			}
			if downloaded {
				c.LastDownloaded = &last
			}

			out = append(out, c)
		}
	}

	slices.SortFunc(out, func(a, b Candidate) int {
		return strings.Compare(a.Path, b.Path)
	})

	return out, nil
}

func matchedAbove(matched map[string]bool, p string) bool {
	for dir := path.Dir(p); dir != "." && dir != "/" && dir != ""; dir = path.Dir(dir) {
		if matched[dir] {
			return true
		}
	}

	return false
}
