// Package project manages projects: a key that repositories name, members in
// roles, and a storage quota over the project's repositories.
package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"

	"relic/internal/access/auth"
	"relic/internal/access/security"
	"relic/internal/platform/database"
)

// Errors.
var (
	ErrNotFound       = errors.New("project not found")
	ErrMemberNotFound = errors.New("project member not found")
	ErrInvalid        = errors.New("invalid project")
)

// Role is what a member may do.
type Role string

// Roles.
const (
	// RoleAdmin works in the project's repositories, and manages its members
	// and repositories.
	RoleAdmin Role = "admin"
	// RoleDeveloper reads, deploys, annotates and deletes in the project's
	// repositories.
	RoleDeveloper Role = "developer"
	// RoleViewer reads the project's repositories.
	RoleViewer Role = "viewer"
)

var roleActions = map[Role][]auth.Action{
	RoleAdmin: {
		auth.ActionRead,
		auth.ActionWrite,
		auth.ActionAnnotate,
		auth.ActionDelete,
	},
	RoleDeveloper: {
		auth.ActionRead,
		auth.ActionWrite,
		auth.ActionAnnotate,
		auth.ActionDelete,
	},
	RoleViewer: {
		auth.ActionRead,
	},
}

var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9]{1,31}$`)

// ValidKey reports whether key has the form of a project key.
func ValidKey(key string) bool { return keyPattern.MatchString(key) }

// DefaultKey stands for no project where a project key is expected.
const DefaultKey = "default"

// IsDefault reports whether key stands for no project: empty or DefaultKey.
func IsDefault(key string) bool { return key == "" || key == DefaultKey }

// Member is a user, or a group, in a role.
type Member struct {
	Name  string `json:"name"`
	Group bool   `json:"group,omitempty"`
	Role  Role   `json:"role"`
}

// Project is a project.
type Project struct {
	Key         string `json:"-"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// StorageQuotaBytes bounds the size of the files in the project's
	// repositories; 0 is no quota.
	StorageQuotaBytes int64     `json:"storageQuotaBytes,omitempty"`
	Members           []Member  `json:"members"`
	Created           time.Time `json:"-"`
	Updated           time.Time `json:"-"`
}

// Role is the strongest role p has in the project, or "".
func (pr Project) Role(p auth.Principal) Role {
	var best Role

	for _, m := range pr.Members {
		if (!m.Group && !p.Scoped && m.Name == p.Username) || (m.Group && slices.Contains(p.Groups, m.Name)) {
			if best == "" || rank(m.Role) > rank(best) {
				best = m.Role
			}
		}
	}

	return best
}

func rank(r Role) int {
	switch r {
	case RoleAdmin:
		return 3
	case RoleDeveloper:
		return 2
	case RoleViewer:
		return 1
	}

	return 0
}

// Service stores projects.
type Service struct {
	db  *database.DB
	now func() time.Time
	// changed runs after a change, so permissions are recompiled.
	changed func()
	// repositoryProjects maps repositories to their projects.
	repositoryProjects  func(context.Context) (map[string]string, error)
	storageRepositories func(key string) []string
}

// New returns a Service.
func New(db *database.DB) *Service {
	return &Service{
		db: db,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

// SetChanged runs fn after every change.
func (s *Service) SetChanged(fn func()) { s.changed = fn }

// SetRepositoryProjects tells the service which repositories belong to which
// project.
func (s *Service) SetRepositoryProjects(fn func(context.Context) (map[string]string, error)) {
	s.repositoryProjects = fn
}

// SetStorageRepositories tells the service where a project's builds and
// releases are stored, so its members' roles cover them.
func (s *Service) SetStorageRepositories(fn func(key string) []string) {
	s.storageRepositories = fn
}

func domainProject(r database.Project) (Project, error) {
	var p Project
	if err := json.Unmarshal(r.Config, &p); err != nil {
		return Project{}, fmt.Errorf("decode project %s: %w", r.Key, err)
	}

	p.Key, p.Created, p.Updated = r.Key, r.Created, r.Updated

	return p, nil
}

// List returns every project.
func (s *Service) List(ctx context.Context) ([]Project, error) {
	rows, err := s.db.ListProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}

	out := make([]Project, 0, len(rows))

	for _, r := range rows {
		p, err := domainProject(r)
		if err != nil {
			return nil, err
		}

		out = append(out, p)
	}

	return out, nil
}

// Get returns one project.
func (s *Service) Get(ctx context.Context, key string) (Project, error) {
	r, err := s.db.GetProject(ctx, key)
	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		return Project{}, fmt.Errorf("%w: %s", ErrNotFound, key)
	}

	if err != nil {
		return Project{}, fmt.Errorf("get project: %w", err)
	}

	return domainProject(r)
}

// Put creates or replaces a project.
func (s *Service) Put(ctx context.Context, p Project) (Project, error) {
	if err := validate(&p); err != nil {
		return Project{}, err
	}

	if err := s.db.WithLock(ctx, projectLock(p.Key), func() error {
		stored, err := s.Get(ctx, p.Key)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}

		if err := s.checkNewMembers(ctx, stored.Members, p.Members); err != nil {
			return err
		}

		return s.store(ctx, p)
	}); err != nil {
		return Project{}, err
	}

	return s.Get(ctx, p.Key)
}

// SetMembers replaces a project's members.
func (s *Service) SetMembers(ctx context.Context, key string, members []Member) (Project, error) {
	return s.changeMembers(ctx, key, func([]Member) ([]Member, error) {
		return members, nil
	})
}

// AddMember adds m to a project, or changes the role of the member it
// names, and leaves the other members as they are.
func (s *Service) AddMember(ctx context.Context, key string, m Member) (Project, error) {
	return s.changeMembers(ctx, key, func(members []Member) ([]Member, error) {
		i := slices.IndexFunc(members, func(existing Member) bool {
			return existing.Name == m.Name && existing.Group == m.Group
		})
		if i < 0 {
			return append(members, m), nil
		}

		members[i] = m

		return members, nil
	})
}

// RemoveMember removes the user, or the group, name from a project, and
// leaves the other members as they are; it fails with ErrMemberNotFound when
// name is not such a member.
func (s *Service) RemoveMember(ctx context.Context, key, name string, group bool) (Project, error) {
	return s.changeMembers(ctx, key, func(members []Member) ([]Member, error) {
		kept := slices.DeleteFunc(members, func(existing Member) bool {
			return existing.Name == name && existing.Group == group
		})
		if len(kept) == len(members) {
			return nil, fmt.Errorf("%w: %s has no %s member %s", ErrMemberNotFound, key, memberKind(group), name)
		}

		return kept, nil
	})
}

// RemoveMemberships removes the user, or the group, name from the members of
// every project, with q.
func (s *Service) RemoveMemberships(ctx context.Context, q database.Querier, name string, group bool) error {
	rows, err := q.ListProjects(ctx)
	if err != nil {
		return fmt.Errorf("list projects: %w", err)
	}

	for _, r := range rows {
		p, err := domainProject(r)
		if err != nil {
			return err
		}

		kept := slices.DeleteFunc(slices.Clone(p.Members), func(m Member) bool {
			return m.Name == name && m.Group == group
		})
		if len(kept) == len(p.Members) {
			continue
		}

		p.Members = kept

		doc, err := json.Marshal(p)
		if err != nil {
			return err
		}

		if err := q.UpsertProject(ctx, database.UpsertProjectParams{
			Key:    p.Key,
			Config: doc,
			Now:    s.now(),
		}); err != nil {
			return fmt.Errorf("put project %s: %w", p.Key, err)
		}
	}

	return nil
}

func memberKind(group bool) string {
	if group {
		return "group"
	}

	return "user"
}

func (s *Service) changeMembers(
	ctx context.Context,
	key string,
	change func([]Member) ([]Member, error),
) (Project, error) {
	if err := s.db.WithLock(ctx, projectLock(key), func() error {
		p, err := s.Get(ctx, key)
		if err != nil {
			return err
		}

		stored := slices.Clone(p.Members)
		if p.Members, err = change(p.Members); err != nil {
			return err
		}

		if err := validate(&p); err != nil {
			return err
		}

		if err := s.checkNewMembers(ctx, stored, p.Members); err != nil {
			return err
		}

		return s.store(ctx, p)
	}); err != nil {
		return Project{}, err
	}

	return s.Get(ctx, key)
}

func (s *Service) checkNewMembers(ctx context.Context, stored, members []Member) error {
	for _, m := range members {
		if slices.ContainsFunc(stored, func(existing Member) bool {
			return existing.Name == m.Name && existing.Group == m.Group
		}) {
			continue
		}

		if err := security.CheckPrincipal(ctx, s.db, m.Name, m.Group); err != nil {
			return err
		}
	}

	return nil
}

func projectLock(key string) string { return "project:" + key }

func (s *Service) store(ctx context.Context, p Project) error {
	doc, err := json.Marshal(p)
	if err != nil {
		return err
	}

	if err := s.db.UpsertProject(ctx, database.UpsertProjectParams{
		Key:    p.Key,
		Config: doc,
		Now:    s.now(),
	}); err != nil {
		return fmt.Errorf("put project: %w", err)
	}

	s.notify()

	return nil
}

func validate(p *Project) error {
	if !ValidKey(p.Key) {
		return fmt.Errorf(
			"%w: key %q: 2 to 32 lower-case letters and digits, starting with a letter",
			ErrInvalid,
			p.Key,
		)
	}

	if p.Name == "" {
		p.Name = p.Key
	}

	if p.StorageQuotaBytes < 0 {
		return fmt.Errorf("%w: a negative storage quota", ErrInvalid)
	}

	seen := map[[2]string]bool{}

	for i, m := range p.Members {
		if m.Name == "" {
			return fmt.Errorf("%w: member %d has no name", ErrInvalid, i+1)
		}

		if _, ok := roleActions[m.Role]; !ok {
			return fmt.Errorf("%w: role %q of %s: admin, developer or viewer", ErrInvalid, m.Role, m.Name)
		}

		kind := "user"
		if m.Group {
			kind = "group"
		}

		k := [2]string{
			kind,
			m.Name,
		}
		if seen[k] {
			return fmt.Errorf("%w: %s %s is listed twice", ErrInvalid, kind, m.Name)
		}

		seen[k] = true
	}

	if p.Members == nil {
		p.Members = []Member{}
	}

	return nil
}

// Delete removes a project; its repositories keep naming it.
func (s *Service) Delete(ctx context.Context, key string) error {
	return s.db.WithLock(ctx, projectLock(key), func() error {
		n, err := s.db.DeleteProject(ctx, key)
		if err != nil {
			return fmt.Errorf("delete project: %w", err)
		}

		if n == 0 {
			return fmt.Errorf("%w: %s", ErrNotFound, key)
		}

		s.notify()

		return nil
	})
}

func (s *Service) notify() {
	if s.changed != nil {
		s.changed()
	}
}

// Administers reports whether p may manage the project key: an
// administrator, or the project's admin.
func (s *Service) Administers(ctx context.Context, p auth.Principal, key string) (bool, error) {
	if p.Admin {
		return true, nil
	}

	if key == "" || p.Anonymous() {
		return false, nil
	}

	pr, err := s.Get(ctx, key)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}

	if err != nil {
		return false, err
	}

	return pr.Role(p) == RoleAdmin, nil
}

// Quota is the storage quota of the project key, 0 for none.
func (s *Service) Quota(ctx context.Context, key string) (int64, error) {
	p, err := s.Get(ctx, key)
	if errors.Is(err, ErrNotFound) {
		return 0, nil
	}

	if err != nil {
		return 0, err
	}

	return p.StorageQuotaBytes, nil
}

// Permissions are the projects' roles as permissions on their repositories,
// one per project, named "project <key>".
func (s *Service) Permissions(ctx context.Context) ([]auth.Permission, error) {
	if s.repositoryProjects == nil {
		return nil, nil
	}

	projects, err := s.List(ctx)
	if err != nil || len(projects) == 0 {
		return nil, err
	}

	repos, err := s.repositoryProjects(ctx)
	if err != nil {
		return nil, err
	}

	byProject := map[string][]string{}
	for repo, key := range repos {
		byProject[key] = append(byProject[key], repo)
	}

	var out []auth.Permission

	for _, p := range projects {
		if len(p.Members) == 0 {
			continue
		}

		perm := auth.Permission{
			Name: "project " + p.Key,
		}

		if names := byProject[p.Key]; len(names) > 0 {
			slices.Sort(names)
			perm.Repository = memberSection(p.Members, names)
		}

		if s.storageRepositories != nil {
			stored := memberSection(p.Members, s.storageRepositories(p.Key))
			perm.Build, perm.Release = stored, stored
		}

		if perm.Repository != nil || perm.Build != nil {
			out = append(out, perm)
		}
	}

	return out, nil
}

func memberSection(members []Member, repositories []string) *auth.Section {
	sec := &auth.Section{
		Selector: auth.Selector{
			Repositories: repositories,
		},
		Users:  map[string][]auth.Action{},
		Groups: map[string][]auth.Action{},
	}

	for _, m := range members {
		if m.Group {
			sec.Groups[m.Name] = roleActions[m.Role]
		} else {
			sec.Users[m.Name] = roleActions[m.Role]
		}
	}

	return sec
}
