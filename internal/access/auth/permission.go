package auth

import (
	"slices"
	"strings"
)

// Action is what a permission grants.
type Action string

// Actions.
const (
	ActionRead     Action = "read"
	ActionWrite    Action = "write"
	ActionAnnotate Action = "annotate"
	ActionDelete   Action = "delete"
	// ActionManage implies every other action.
	ActionManage Action = "manage"
	// ActionPromote promotes and distributes releases.
	ActionPromote Action = "promote"
)

// Actions lists every action.
var Actions = []Action{
	ActionRead,
	ActionWrite,
	ActionAnnotate,
	ActionDelete,
	ActionManage,
	ActionPromote,
}

// RepositorySet selects repositories by class rather than by name.
type RepositorySet string

// Repository sets.
const (
	SetAll    RepositorySet = "all"
	SetLocal  RepositorySet = "local"
	SetRemote RepositorySet = "remote"
)

// RepositorySets lists every repository set.
var RepositorySets = []RepositorySet{
	SetAll,
	SetLocal,
	SetRemote,
}

// Selector is the repositories a rule covers: whole sets, and repositories
// by name.
type Selector struct {
	Sets         []RepositorySet `json:"sets,omitempty"`
	Repositories []string        `json:"repositories,omitempty"`
}

// Section is one rule of a permission, compiled: what it covers and who may
// do what there.
type Section struct {
	Selector Selector
	Include  []string // Ant patterns; empty means "**"
	Exclude  []string
	Users    map[string][]Action
	Groups   map[string][]Action
}

// Permission is a compiled permission: its rule for each kind of resource.
type Permission struct {
	Name       string
	Repository *Section
	Build      *Section
	Release    *Section
}

// Subject is who asks.
type Subject struct {
	User   string
	Groups []string
	Admin  bool
}

// Repository identifies a repository for evaluation.
type Repository struct {
	Name  string
	Class string
}

// Permissions evaluates permissions.
type Permissions struct {
	All []Permission
	// AdminOnly are repositories only administrators may use, whatever
	// permissions say.
	AdminOnly []string
}

func (p Permissions) adminOnly(repo Repository) bool { return slices.Contains(p.AdminOnly, repo.Name) }

// CanInRepository reports whether s may perform a on path in repo.
func (p Permissions) CanInRepository(s Subject, repo Repository, itemPath string, a Action) bool {
	if s.Admin {
		return true
	}

	if p.adminOnly(repo) || dotSegment(itemPath) {
		return false
	}

	for _, t := range p.All {
		if t.Repository != nil && t.Repository.coversRepository(repo) && t.Repository.coversPath(itemPath) &&
			t.Repository.grants(s, a) {
			return true
		}
	}

	return false
}

// CanReadRepository reports whether s may read anything in repo, e.g. to list it.
func (p Permissions) CanReadRepository(s Subject, repo Repository) bool {
	return p.CanAnywhereInRepository(s, repo, ActionRead)
}

// CanAnywhereInRepository reports whether s may perform a on some path in repo.
func (p Permissions) CanAnywhereInRepository(s Subject, repo Repository, a Action) bool {
	if s.Admin {
		return true
	}

	if p.adminOnly(repo) {
		return false
	}

	for _, t := range p.All {
		if t.Repository != nil && t.Repository.coversRepository(repo) && t.Repository.grants(s, a) {
			return true
		}
	}

	return false
}

// CanReadAll reports whether s may read every path in repo: some permission
// grants read on it with no include narrower than "**" and no excludes.
func (p Permissions) CanReadAll(s Subject, repo Repository) bool {
	if s.Admin {
		return true
	}

	if p.adminOnly(repo) {
		return false
	}

	for _, t := range p.All {
		sec := t.Repository
		if sec == nil || !sec.coversRepository(repo) || !sec.grants(s, ActionRead) {
			continue
		}

		everything := len(sec.Include) == 0 || slices.Contains(sec.Include, "**")
		if everything && !slices.ContainsFunc(sec.Exclude, func(p string) bool {
			return p != ""
		}) {
			return true
		}
	}

	return false
}

// CanBuild reports whether s may perform a on the named build whose
// build info is stored in the named repository.
func (p Permissions) CanBuild(s Subject, repository, name string, a Action) bool {
	if s.Admin {
		return true
	}

	stored := Repository{
		Name:  repository,
		Class: "local",
	}
	for _, t := range p.All {
		if t.Build != nil && t.Build.coversRepository(stored) && t.Build.coversPath(name+"/x") &&
			t.Build.grants(s, a) {
			return true
		}
	}

	return false
}

// CanRelease reports whether s may perform a on a version of a release
// stored in the named repository.
func (p Permissions) CanRelease(s Subject, repository, name, version string, a Action) bool {
	if s.Admin {
		return true
	}

	stored := Repository{
		Name:  repository,
		Class: "local",
	}
	for _, t := range p.All {
		if t.Release != nil && t.Release.coversRepository(stored) && t.Release.coversPath(name+"/"+version) &&
			t.Release.grants(s, a) {
			return true
		}
	}

	return false
}

func (sec *Section) coversRepository(r Repository) bool {
	for _, set := range sec.Selector.Sets {
		switch set {
		case SetAll:
			return true
		case SetLocal, SetRemote:
			if string(set) == r.Class {
				return true
			}
		}
	}

	return slices.Contains(sec.Selector.Repositories, r.Name)
}

func (sec *Section) coversPath(p string) bool {
	includes := sec.Include
	if len(includes) == 0 {
		includes = []string{
			"**",
		}
	}

	if !slices.ContainsFunc(includes, func(pat string) bool {
		return MatchesAntPattern(pat, p)
	}) {
		return false
	}

	return !slices.ContainsFunc(sec.Exclude, func(pat string) bool {
		return pat != "" && MatchesAntPattern(pat, p)
	})
}

// Write and delete do not imply read.
func (sec *Section) grants(s Subject, a Action) bool {
	has := func(actions []Action) bool {
		return slices.Contains(actions, a) || slices.Contains(actions, ActionManage)
	}
	if s.User != "" && has(sec.Users[s.User]) {
		return true
	}

	for _, g := range s.Groups {
		if has(sec.Groups[g]) {
			return true
		}
	}

	return false
}

// Grant is what one user or group may do on an item, and the permissions
// that grant it.
type Grant struct {
	Principal   string
	Group       bool
	Actions     []Action
	Permissions []string
}

var repoActions = []Action{
	ActionRead,
	ActionAnnotate,
	ActionWrite,
	ActionDelete,
	ActionManage,
}

// Effective lists who may do what on itemPath in repo according to the
// permissions (administrators, who may do everything, are not listed).
func (p Permissions) Effective(repo Repository, itemPath string) []Grant {
	if p.adminOnly(repo) || dotSegment(itemPath) {
		return []Grant{}
	}

	type key struct {
		name  string
		group bool
	}

	acts := map[key]map[Action]bool{}
	targets := map[key][]string{}
	add := func(k key, actions []Action, target string) {
		if acts[k] == nil {
			acts[k] = map[Action]bool{}
		}

		for _, a := range actions {
			acts[k][a] = true
			if a == ActionManage {
				for _, implied := range repoActions {
					acts[k][implied] = true
				}
			}
		}

		if !slices.Contains(targets[k], target) {
			targets[k] = append(targets[k], target)
		}
	}

	for _, t := range p.All {
		sec := t.Repository
		if sec == nil || !sec.coversRepository(repo) || !sec.coversPath(itemPath) {
			continue
		}

		for u, a := range sec.Users {
			add(key{
				u,
				false,
			}, a, t.Name)
		}

		for g, a := range sec.Groups {
			add(key{
				g,
				true,
			}, a, t.Name)
		}
	}

	out := make([]Grant, 0, len(acts))
	for k, set := range acts {
		g := Grant{
			Principal:   k.name,
			Group:       k.group,
			Permissions: targets[k],
			Actions:     []Action{},
		}

		for _, a := range repoActions {
			if set[a] {
				g.Actions = append(g.Actions, a)
			}
		}

		out = append(out, g)
	}

	slices.SortFunc(out, func(a, b Grant) int {
		if a.Group != b.Group {
			if a.Group {
				return 1
			}

			return -1
		}

		return strings.Compare(a.Principal, b.Principal)
	})

	return out
}
