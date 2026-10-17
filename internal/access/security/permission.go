package security

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"relic/internal/access/auth"
	"relic/internal/platform/database"
)

// Permission grants users and groups actions on repositories, builds and
// releases: at most one rule per kind of resource.
type Permission struct {
	// Name identifies the permission; it is not part of the stored settings.
	Name  string `json:"-"`
	Rules []Rule `json:"rules"`
}

// ResourceKind is what a rule covers.
type ResourceKind string

// Resource kinds.
const (
	// ResourceRepository covers items in repositories.
	ResourceRepository ResourceKind = "repository"
	// ResourceBuild covers published builds; patterns match "<name>/**".
	ResourceBuild ResourceKind = "build"
	// ResourceRelease covers release versions; patterns match
	// "<name>/<version>".
	ResourceRelease ResourceKind = "release"
)

// Rule grants actions on one kind of resource.
type Rule struct {
	Resource ResourceKind `json:"resource"`
	// Selector is the repositories the rule covers (for builds and releases,
	// the repositories that store them).
	Selector auth.Selector `json:"selector"`
	// Include and Exclude are Ant patterns on paths within the repositories;
	// an empty Include covers everything.
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
	Grants  []Grant  `json:"grants"`
}

// Grant is what one user or one group may do.
type Grant struct {
	User    string        `json:"user,omitempty"`
	Group   string        `json:"group,omitempty"`
	Actions []auth.Action `json:"actions"`
}

// Rule returns the permission's rule for a kind of resource.
func (p Permission) Rule(kind ResourceKind) (Rule, bool) {
	for _, r := range p.Rules {
		if r.Resource == kind {
			return r, true
		}
	}

	return Rule{}, false
}

// Validate checks a permission before it is stored, trimming list values
// and dropping empty and repeated ones.
func (p *Permission) Validate() error {
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("%w: a permission needs a name", ErrInvalid)
	}

	if p.Rules == nil {
		p.Rules = []Rule{}
	}

	seen := map[ResourceKind]bool{}

	for i := range p.Rules {
		r := &p.Rules[i]
		switch r.Resource {
		case ResourceRepository, ResourceBuild, ResourceRelease:
		default:
			return fmt.Errorf("%w: unknown resource %q", ErrInvalid, r.Resource)
		}

		if seen[r.Resource] {
			return fmt.Errorf("%w: more than one %s rule", ErrInvalid, r.Resource)
		}

		seen[r.Resource] = true
		for _, set := range r.Selector.Sets {
			if !slices.Contains(auth.RepositorySets, set) {
				return fmt.Errorf("%w: unknown repository set %q", ErrInvalid, set)
			}
		}

		var sets []auth.RepositorySet

		for _, set := range r.Selector.Sets {
			if !slices.Contains(sets, set) {
				sets = append(sets, set)
			}
		}

		r.Selector.Sets = sets
		r.Selector.Repositories = splitList(r.Selector.Repositories)

		r.Include, r.Exclude = splitList(r.Include), splitList(r.Exclude)
		if r.Grants == nil {
			r.Grants = []Grant{}
		}

		for j := range r.Grants {
			g := &r.Grants[j]

			g.User, g.Group = strings.TrimSpace(g.User), strings.TrimSpace(g.Group)
			if (g.User == "") == (g.Group == "") {
				return fmt.Errorf("%w: a grant names one user or one group", ErrInvalid)
			}

			clean := []auth.Action{}

			for _, a := range g.Actions {
				a = auth.Action(strings.TrimSpace(string(a)))
				if a == "" || slices.Contains(clean, a) {
					continue
				}

				if !slices.Contains(auth.Actions, a) {
					return fmt.Errorf("%w: unknown action %q", ErrInvalid, a)
				}

				clean = append(clean, a)
			}

			g.Actions = clean
		}
	}

	return nil
}

func (p Permission) compile() auth.Permission {
	section := func(kind ResourceKind) *auth.Section {
		r, ok := p.Rule(kind)
		if !ok {
			return nil
		}

		sec := &auth.Section{
			Selector: r.Selector,
			Include:  r.Include,
			Exclude:  r.Exclude,
			Users:    map[string][]auth.Action{},
			Groups:   map[string][]auth.Action{},
		}
		for _, g := range r.Grants {
			if g.User != "" {
				sec.Users[g.User] = append(sec.Users[g.User], g.Actions...)
			} else {
				sec.Groups[g.Group] = append(sec.Groups[g.Group], g.Actions...)
			}
		}

		return sec
	}

	return auth.Permission{
		Name:       p.Name,
		Repository: section(ResourceRepository),
		Build:      section(ResourceBuild),
		Release:    section(ResourceRelease),
	}
}

// CreatePermission stores a new permission; ErrExists if the name is taken.
func (s *Service) CreatePermission(ctx context.Context, pt Permission) error {
	if err := pt.Validate(); err != nil {
		return err
	}

	cfg, err := json.Marshal(pt)
	if err != nil {
		return err
	}

	err = s.db.InsertPermission(ctx, database.InsertPermissionParams{
		Name:   pt.Name,
		Config: cfg,
	})
	if database.IsUniqueViolation(err) {
		return fmt.Errorf("%w: permission %q", ErrExists, pt.Name)
	}

	s.invalidate()

	return err
}

// UpdatePermission replaces an existing permission.
func (s *Service) UpdatePermission(ctx context.Context, pt Permission) error {
	if err := pt.Validate(); err != nil {
		return err
	}

	cfg, err := json.Marshal(pt)
	if err != nil {
		return err
	}

	n, err := s.db.UpdatePermission(ctx, database.UpdatePermissionParams{
		Name:   pt.Name,
		Config: cfg,
	})
	if err != nil {
		return err
	}

	if n == 0 {
		return fmt.Errorf("%w: permission %q", ErrNotFound, pt.Name)
	}

	s.invalidate()

	return nil
}

// GetPermission returns one permission.
func (s *Service) GetPermission(ctx context.Context, name string) (Permission, error) {
	row, err := s.db.GetPermission(ctx, name)
	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		return Permission{}, fmt.Errorf("%w: permission %q", ErrNotFound, name)
	}

	if err != nil {
		return Permission{}, fmt.Errorf("get permission %q: %w", name, err)
	}

	return decodePermission(row.Name, row.Config)
}

func decodePermission(name string, config []byte) (Permission, error) {
	p := Permission{
		Name: name,
	}
	if err := json.Unmarshal(config, &p); err != nil {
		return Permission{}, fmt.Errorf("decode permission %q: %w", name, err)
	}

	return p, nil
}

// ListPermissions returns every permission name.
func (s *Service) ListPermissions(ctx context.Context) ([]string, error) {
	rows, err := s.db.ListPermissions(ctx)
	if err != nil {
		return nil, err
	}

	names := make([]string, len(rows))
	for i, r := range rows {
		names[i] = r.Name
	}

	return names, nil
}

// DeletePermission removes a permission.
func (s *Service) DeletePermission(ctx context.Context, name string) error {
	n, err := s.db.DeletePermission(ctx, name)
	if err != nil {
		return err
	}

	if n == 0 {
		return fmt.Errorf("%w: permission %q", ErrNotFound, name)
	}

	s.invalidate()

	return nil
}
