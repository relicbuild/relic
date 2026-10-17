// Package permissions translates the adapter's permission-target documents
// to and from Relic's permissions.
package permissions

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"relic/internal/access/auth"
	"relic/internal/access/security"
)

// Target is the v2 permission-target document.
type Target struct {
	Name          string   `json:"name"`
	Repo          *Section `json:"repo,omitempty"`
	Build         *Section `json:"build,omitempty"`
	ReleaseBundle *Section `json:"releaseBundle,omitempty"`
}

// Section is one resource section of a Target.
type Section struct {
	IncludePatterns []string `json:"include-patterns"`
	ExcludePatterns []string `json:"exclude-patterns"`
	Repositories    []string `json:"repositories"`
	Actions         *Actions `json:"actions,omitempty"`
}

// Actions maps principals to granted actions.
type Actions struct {
	Users  map[string][]string `json:"users,omitempty"`
	Groups map[string][]string `json:"groups,omitempty"`
}

var sets = map[string]auth.RepositorySet{
	"ANY":        auth.SetAll,
	"ANY LOCAL":  auth.SetLocal,
	"ANY REMOTE": auth.SetRemote,
}

var (
	actionsFrom = map[string]auth.Action{
		"distribute": auth.ActionPromote,
	}
	actionsTo = map[auth.Action]string{
		auth.ActionPromote: "distribute",
	}
)

const ignoredAction = "managedXrayMeta"

// FromArtifactory reads a document sent for name.
func FromArtifactory(name string, doc []byte) (security.Permission, error) {
	var t Target
	if err := json.Unmarshal(doc, &t); err != nil {
		return security.Permission{}, fmt.Errorf("%w: %w", security.ErrInvalid, err)
	}

	if t.Name != "" && t.Name != name {
		return security.Permission{}, fmt.Errorf("%w: name %q does not match URL %q", security.ErrInvalid, t.Name, name)
	}

	p := security.Permission{
		Name:  name,
		Rules: []security.Rule{},
	}

	for _, s := range []struct {
		kind security.ResourceKind
		sec  *Section
	}{
		{
			security.ResourceRepository,
			t.Repo,
		},
		{
			security.ResourceBuild,
			t.Build,
		},
		{
			security.ResourceRelease,
			t.ReleaseBundle,
		},
	} {
		if s.sec != nil {
			p.Rules = append(p.Rules, ruleFrom(s.kind, s.sec))
		}
	}

	return p, p.Validate()
}

func ruleFrom(kind security.ResourceKind, sec *Section) security.Rule {
	r := security.Rule{
		Resource: kind,
		Include:  sec.IncludePatterns,
		Exclude:  sec.ExcludePatterns,
		Grants:   []security.Grant{},
	}
	for _, target := range sec.Repositories {
		if set, ok := sets[strings.TrimSpace(target)]; ok {
			r.Selector.Sets = append(r.Selector.Sets, set)
		} else {
			r.Selector.Repositories = append(r.Selector.Repositories, target)
		}
	}

	if sec.Actions != nil {
		for _, u := range slices.Sorted(maps.Keys(sec.Actions.Users)) {
			r.Grants = append(r.Grants, security.Grant{
				User:    u,
				Actions: actionsOf(sec.Actions.Users[u]),
			})
		}

		for _, g := range slices.Sorted(maps.Keys(sec.Actions.Groups)) {
			r.Grants = append(r.Grants, security.Grant{
				Group:   g,
				Actions: actionsOf(sec.Actions.Groups[g]),
			})
		}
	}

	return r
}

func actionsOf(names []string) []auth.Action {
	out := []auth.Action{}

	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == ignoredAction {
			continue
		}

		if a, ok := actionsFrom[n]; ok {
			out = append(out, a)
		} else {
			out = append(out, auth.Action(n))
		}
	}

	return out
}

// ToArtifactory renders p as a permission-target document.
func ToArtifactory(p security.Permission) ([]byte, error) {
	t := Target{
		Name: p.Name,
	}
	if r, ok := p.Rule(security.ResourceRepository); ok {
		t.Repo = sectionOf(r)
	}

	if r, ok := p.Rule(security.ResourceBuild); ok {
		t.Build = sectionOf(r)
	}

	if r, ok := p.Rule(security.ResourceRelease); ok {
		t.ReleaseBundle = sectionOf(r)
	}

	b, err := json.Marshal(t)
	if err != nil {
		return nil, fmt.Errorf("encode permission %q: %w", p.Name, err)
	}

	return b, nil
}

func sectionOf(r security.Rule) *Section {
	sec := &Section{
		Repositories:    []string{},
		IncludePatterns: nonNil(r.Include),
		ExcludePatterns: nonNil(r.Exclude),
		Actions:         &Actions{},
	}
	for _, set := range r.Selector.Sets {
		for name, s := range sets {
			if s == set {
				sec.Repositories = append(sec.Repositories, name)
			}
		}
	}

	sec.Repositories = append(sec.Repositories, r.Selector.Repositories...)
	for _, g := range r.Grants {
		names := make([]string, 0, len(g.Actions))
		for _, a := range g.Actions {
			if n, ok := actionsTo[a]; ok {
				names = append(names, n)
			} else {
				names = append(names, string(a))
			}
		}

		if g.User != "" {
			if sec.Actions.Users == nil {
				sec.Actions.Users = map[string][]string{}
			}

			sec.Actions.Users[g.User] = names
		} else {
			if sec.Actions.Groups == nil {
				sec.Actions.Groups = map[string][]string{}
			}

			sec.Actions.Groups[g.Group] = names
		}
	}

	return sec
}

func nonNil(l []string) []string {
	if l == nil {
		return []string{}
	}

	return l
}
