// Package ldapsettings translates the adapter's LDAP settings and LDAP
// group settings documents to and from Relic's directories and group
// mappings.
package ldapsettings

import (
	"encoding/json"
	"strings"

	"relic/internal/access/ldap"
)

// Extension is the extension that holds the Access API settings Relic
// keeps but does not act on.
const Extension = "artifactory"

// Search is the search part of Settings.
type Search struct {
	Filter          string `json:"search_filter"`
	Base            string `json:"search_base"`
	SubTree         bool   `json:"search_sub_tree"`
	ManagerDN       string `json:"manager_dn"`
	ManagerPassword string `json:"manager_password"`
}

// Settings is an LDAP settings document.
type Settings struct {
	Key            string `json:"key"`
	Enabled        bool   `json:"enabled"`
	URL            string `json:"ldap_url"`
	UserDNPattern  string `json:"user_dn_pattern"`
	Search         Search `json:"search"`
	AutoCreateUser bool   `json:"auto_create_user"`
	EmailAttribute string `json:"email_attribute"`
	settingsExtras
	// UseStartTLS is a Relic extension: StartTLS on an ldap:// URL.
	UseStartTLS bool `json:"use_start_tls"`
}

type settingsExtras struct {
	PositionProtection       bool `json:"ldap_position_protection"`
	AllowUserToAccessProfile bool `json:"allow_user_to_access_profile"`
	PagingSupportEnabled     bool `json:"paging_support_enabled"`
}

// GroupSettings is an LDAP group settings document.
type GroupSettings struct {
	Name                 string `json:"name"`
	EnabledLdap          string `json:"enabled_ldap"`
	GroupBaseDN          string `json:"group_base_dn"`
	GroupNameAttribute   string `json:"group_name_attribute"`
	GroupMemberAttribute string `json:"group_member_attribute"`
	SubTree              bool   `json:"sub_tree"`
	groupExtras
	Filter               string `json:"filter"`
	DescriptionAttribute string `json:"description_attribute"`
	Strategy             string `json:"strategy"`
}

type groupExtras struct {
	ForceAttributeSearch bool `json:"force_attribute_search"`
}

var strategies = map[string]ldap.GroupStrategy{
	"hierarchical": ldap.StrategyHierarchy,
}

// FromArtifactory reads a settings document.
func FromArtifactory(s Settings) ldap.Server {
	return ldap.Server{
		Name:           s.Key,
		Enabled:        s.Enabled,
		URL:            s.URL,
		UserDNPattern:  s.UserDNPattern,
		Search:         ldap.Search(s.Search),
		AutoCreateUser: s.AutoCreateUser,
		EmailAttribute: s.EmailAttribute,
		StartTLS:       s.UseStartTLS,
		Extensions:     extension(s.settingsExtras),
	}
}

// ToArtifactory renders a directory as a settings document.
func ToArtifactory(d ldap.Server) Settings {
	s := Settings{
		Key:            d.Name,
		Enabled:        d.Enabled,
		URL:            d.URL,
		UserDNPattern:  d.UserDNPattern,
		Search:         Search(d.Search),
		AutoCreateUser: d.AutoCreateUser,
		EmailAttribute: d.EmailAttribute,
		UseStartTLS:    d.StartTLS,
	}
	readExtension(d.Extensions, &s.settingsExtras)

	return s
}

// GroupFromArtifactory reads a group settings document.
func GroupFromArtifactory(g GroupSettings) ldap.GroupMapping {
	strategy := strings.ToLower(g.Strategy)

	s, ok := strategies[strategy]
	if !ok {
		s = ldap.GroupStrategy(strategy)
	}

	return ldap.GroupMapping{
		Name:                 g.Name,
		Server:               g.EnabledLdap,
		BaseDN:               g.GroupBaseDN,
		NameAttribute:        g.GroupNameAttribute,
		MemberAttribute:      g.GroupMemberAttribute,
		SubTree:              g.SubTree,
		Filter:               g.Filter,
		DescriptionAttribute: g.DescriptionAttribute,
		Strategy:             s,
		Extensions:           extension(g.groupExtras),
	}
}

// GroupToArtifactory renders a group mapping as a group settings document.
func GroupToArtifactory(m ldap.GroupMapping) GroupSettings {
	strategy := string(m.Strategy)
	for name, s := range strategies {
		if s == m.Strategy {
			strategy = name
		}
	}

	g := GroupSettings{
		Name:                 m.Name,
		EnabledLdap:          m.Server,
		GroupBaseDN:          m.BaseDN,
		GroupNameAttribute:   m.NameAttribute,
		GroupMemberAttribute: m.MemberAttribute,
		SubTree:              m.SubTree,
		Filter:               m.Filter,
		DescriptionAttribute: m.DescriptionAttribute,
		Strategy:             strategy,
	}
	readExtension(m.Extensions, &g.groupExtras)

	return g
}

func extension[T comparable](extras T) map[string]json.RawMessage {
	var zero T
	if extras == zero {
		return nil
	}

	raw, err := json.Marshal(extras)
	if err != nil {
		return nil
	}

	return map[string]json.RawMessage{
		Extension: raw,
	}
}

func readExtension[T any](ext map[string]json.RawMessage, extras *T) {
	if raw := ext[Extension]; len(raw) > 0 {
		_ = json.Unmarshal(raw, extras)
	}
}
