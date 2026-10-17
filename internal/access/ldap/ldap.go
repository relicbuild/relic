// Package ldap authenticates users against LDAP directories and reads their
// groups, configured with LDAP server and group mapping settings.
package ldap

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	"relic/internal/platform/telemetry"

	goldap "github.com/go-ldap/ldap/v3"
)

var (
	// ErrInvalidCredentials means the directory has no such user, or the
	// password does not bind.
	ErrInvalidCredentials = errors.New("ldap: invalid credentials")
	// ErrUnavailable means the directory could not be asked.
	ErrUnavailable = errors.New("ldap: directory unavailable")
	// ErrInvalid reports a settings document Relic cannot use.
	ErrInvalid = errors.New("ldap: invalid settings")
)

const timeout = 10 * time.Second

// Search is how users are found when no user DN pattern is set.
type Search struct {
	Filter          string `json:"filter"`
	Base            string `json:"base"`
	SubTree         bool   `json:"subTree"`
	ManagerDN       string `json:"managerDn"`
	ManagerPassword string `json:"managerPassword"`
}

// Server is how Relic reaches an LDAP directory and finds users there.
type Server struct {
	Name           string `json:"name"`
	Enabled        bool   `json:"enabled"`
	URL            string `json:"url"`
	UserDNPattern  string `json:"userDnPattern"`
	Search         Search `json:"search"`
	AutoCreateUser bool   `json:"autoCreateUser"`
	EmailAttribute string `json:"emailAttribute"`
	// StartTLS upgrades an ldap:// connection with StartTLS; ldaps:// URLs
	// use TLS from the start.
	StartTLS bool `json:"startTls"`
	// Extensions keeps settings only an API adapter understands, by adapter
	// name.
	Extensions map[string]json.RawMessage `json:"extensions,omitempty"`
}

// GroupStrategy is how a directory records group membership.
type GroupStrategy string

// Group strategies.
const (
	StrategyStatic    GroupStrategy = "static"
	StrategyDynamic   GroupStrategy = "dynamic"
	StrategyHierarchy GroupStrategy = "hierarchy"
)

// GroupMapping is how Relic reads a directory's groups into Relic groups.
type GroupMapping struct {
	Name string `json:"name"`
	// Server names the LDAP server it reads.
	Server               string        `json:"server"`
	BaseDN               string        `json:"baseDn"`
	NameAttribute        string        `json:"nameAttribute"`
	MemberAttribute      string        `json:"memberAttribute"`
	SubTree              bool          `json:"subTree"`
	Filter               string        `json:"filter"`
	DescriptionAttribute string        `json:"descriptionAttribute"`
	Strategy             GroupStrategy `json:"strategy"`
	// Extensions keeps settings only an API adapter understands.
	Extensions map[string]json.RawMessage `json:"extensions,omitempty"`
}

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func validKey(s string) bool { return keyPattern.MatchString(s) }

// Validate checks what a directory lookup needs.
func (s Server) Validate() error {
	if !validKey(s.Name) {
		return fmt.Errorf("%w: name must be 1-64 letters, digits, '-', '_' or '.'", ErrInvalid)
	}

	if _, _, err := s.endpoint(); err != nil {
		return err
	}

	if s.UserDNPattern == "" && s.Search.Filter == "" {
		return fmt.Errorf("%w: set a user DN pattern or a search filter", ErrInvalid)
	}

	for _, p := range []string{
		s.UserDNPattern,
		s.Search.Filter,
	} {
		if p != "" && !strings.Contains(p, "{0}") {
			return fmt.Errorf("%w: %q must contain {0} for the user name", ErrInvalid, p)
		}
	}

	if s.Search.Filter != "" {
		if _, err := goldap.CompileFilter(userFilter(s.Search.Filter, "x")); err != nil {
			return fmt.Errorf("%w: search filter: %w", ErrInvalid, err)
		}
	}

	return nil
}

// Validate checks a group settings document.
func (g GroupMapping) Validate() error {
	if !validKey(g.Name) {
		return fmt.Errorf("%w: name must be 1-64 letters, digits, '-', '_' or '.'", ErrInvalid)
	}

	if g.Server == "" || g.NameAttribute == "" {
		return fmt.Errorf("%w: the directory and the group name attribute are required", ErrInvalid)
	}

	switch g.strategy() {
	case StrategyStatic:
		if g.MemberAttribute == "" {
			return fmt.Errorf("%w: the static strategy needs the group member attribute", ErrInvalid)
		}
	case StrategyDynamic:
		if g.MemberAttribute == "" {
			return fmt.Errorf(
				"%w: the dynamic strategy needs the member attribute (the user attribute listing groups)",
				ErrInvalid,
			)
		}
	case StrategyHierarchy:
	default:
		return fmt.Errorf("%w: strategy must be static, dynamic or hierarchy", ErrInvalid)
	}

	if g.Filter != "" {
		if _, err := goldap.CompileFilter(g.filter()); err != nil {
			return fmt.Errorf("%w: filter: %w", ErrInvalid, err)
		}
	}

	return nil
}

func (g GroupMapping) strategy() GroupStrategy {
	if g.Strategy == "" {
		return StrategyStatic
	}

	return g.Strategy
}

func (g GroupMapping) filter() string {
	f := strings.TrimSpace(g.Filter)
	if f == "" {
		return "(objectClass=*)"
	}

	if !strings.HasPrefix(f, "(") {
		f = "(" + f + ")"
	}

	return f
}

func (s Server) endpoint() (string, string, error) {
	u, err := url.Parse(s.URL)
	if err != nil || (u.Scheme != "ldap" && u.Scheme != "ldaps") || u.Host == "" {
		return "", "", fmt.Errorf("%w: the URL must be ldap://host[:port]/<base DN> or ldaps://…", ErrInvalid)
	}

	base, err := url.PathUnescape(strings.TrimPrefix(u.EscapedPath(), "/"))
	if err != nil {
		return "", "", fmt.Errorf("%w: the URL's base DN: %w", ErrInvalid, err)
	}

	return u.Scheme + "://" + u.Host, base, nil
}

func under(rel, base string) string {
	switch {
	case rel == "":
		return base
	case base == "":
		return rel
	default:
		return rel + "," + base
	}
}

// Entry is an authenticated directory user.
type Entry struct {
	DN     string
	Email  string
	Groups []string
}

// Client talks to one LDAP server.
type Client struct {
	Server   Server
	Mappings []GroupMapping
	// TLS is used for ldaps:// and StartTLS; ServerName is filled in.
	TLS *tls.Config
}

func (d Client) dial(ctx context.Context) (tracedConn, error) {
	server, _, err := d.Server.endpoint()
	if err != nil {
		return tracedConn{}, err
	}

	u, _ := url.Parse(server)

	cfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}
	if d.TLS != nil {
		cfg = d.TLS.Clone()
	}

	cfg.ServerName = u.Hostname()
	end := telemetry.StartLDAP(ctx, "dial")

	conn, err := goldap.DialURL(
		server,
		goldap.DialWithDialer(&net.Dialer{
			Timeout: timeout,
		}),
		goldap.DialWithTLSConfig(cfg),
	)
	if err != nil {
		end("error")
		return tracedConn{}, fmt.Errorf("%w: dial %s: %w", ErrUnavailable, server, err)
	}

	conn.SetTimeout(timeout)

	if u.Scheme == "ldap" && d.Server.StartTLS {
		if err := conn.StartTLS(cfg); err != nil {
			end("error")

			_ = conn.Close()

			return tracedConn{}, fmt.Errorf("%w: StartTLS with %s: %w", ErrUnavailable, server, err)
		}
	}

	end("success")

	return tracedConn{
		Conn: conn,
		ctx:  ctx,
	}, nil
}

// Authenticate finds username in the directory and binds as it with
// password.
func (d Client) Authenticate(ctx context.Context, username, password string) (Entry, error) {
	// An empty password is an "unauthenticated bind" (RFC 4513 §5.1.2),
	// which many servers accept for any DN.
	if username == "" || password == "" {
		return Entry{}, ErrInvalidCredentials
	}

	_, base, err := d.Server.endpoint()
	if err != nil {
		return Entry{}, err
	}

	conn, err := d.dial(ctx)
	if err != nil {
		return Entry{}, err
	}

	defer func() {
		_ = conn.Close()
	}()

	emailAttr := d.Server.EmailAttribute

	var (
		entry Entry
		attrs *goldap.Entry
	)

	if d.Server.UserDNPattern != "" {
		entry.DN = under(strings.ReplaceAll(d.Server.UserDNPattern, "{0}", goldap.EscapeDN(username)), base)
	} else {
		if err := d.bindManager(conn); err != nil {
			return Entry{}, err
		}

		attrs, err = d.findUser(conn, username, base)
		if err != nil {
			return Entry{}, err
		}

		entry.DN = attrs.DN
	}

	if err := conn.Bind(entry.DN, password); err != nil {
		if goldap.IsErrorWithCode(err, goldap.LDAPResultInvalidCredentials) ||
			goldap.IsErrorWithCode(err, goldap.LDAPResultNoSuchObject) {
			return Entry{}, fmt.Errorf("%w: the bind as the user was refused", ErrInvalidCredentials)
		}

		return Entry{}, fmt.Errorf("%w: bind as user: %w", ErrUnavailable, err)
	}

	if err := d.bindManager(conn); err != nil {
		return Entry{}, err
	}

	if attrs == nil {
		attrs, err = d.readEntry(conn, entry.DN)
		if err != nil {
			return Entry{}, err
		}
	}

	if emailAttr != "" {
		entry.Email = attrs.GetAttributeValue(emailAttr)
	}

	entry.Groups, err = d.groups(conn, attrs, base)
	if err != nil {
		return Entry{}, err
	}

	return entry, nil
}

func (d Client) bindManager(conn tracedConn) error {
	if d.Server.Search.ManagerDN == "" {
		return nil
	}

	if err := conn.Bind(d.Server.Search.ManagerDN, d.Server.Search.ManagerPassword); err != nil {
		return fmt.Errorf("%w: bind as manager: %w", ErrUnavailable, err)
	}

	return nil
}

func (d Client) attributes() []string {
	out := []string{
		"dn",
	}
	if d.Server.EmailAttribute != "" {
		out = append(out, d.Server.EmailAttribute)
	}

	for _, g := range d.Mappings {
		if g.strategy() == StrategyDynamic {
			out = append(out, g.MemberAttribute)
		}
	}

	return out
}

func (d Client) findUser(conn tracedConn, username, base string) (*goldap.Entry, error) {
	scope := goldap.ScopeSingleLevel
	if d.Server.Search.SubTree {
		scope = goldap.ScopeWholeSubtree
	}

	filter := userFilter(d.Server.Search.Filter, goldap.EscapeFilter(username))

	res, err := conn.Search(goldap.NewSearchRequest(under(d.Server.Search.Base, base), scope, goldap.NeverDerefAliases,
		2, int(timeout/time.Second), false, filter, d.attributes(), nil))
	if err != nil && !goldap.IsErrorWithCode(err, goldap.LDAPResultSizeLimitExceeded) {
		if goldap.IsErrorWithCode(err, goldap.LDAPResultNoSuchObject) {
			return nil, fmt.Errorf("%w: search base does not exist", ErrInvalidCredentials)
		}

		return nil, fmt.Errorf("%w: search user: %w", ErrUnavailable, err)
	}

	if res == nil || len(res.Entries) != 1 {
		n := 0
		if res != nil {
			n = len(res.Entries)
		}

		return nil, fmt.Errorf("%w: the user search found %d entries, not one", ErrInvalidCredentials, n)
	}

	return res.Entries[0], nil
}

func userFilter(filter, escaped string) string {
	f := strings.ReplaceAll(strings.TrimSpace(filter), "{0}", escaped)
	if !strings.HasPrefix(f, "(") {
		f = "(" + f + ")"
	}

	return f
}

func (d Client) readEntry(conn tracedConn, dn string) (*goldap.Entry, error) {
	res, err := conn.Search(goldap.NewSearchRequest(dn, goldap.ScopeBaseObject, goldap.NeverDerefAliases,
		1, int(timeout/time.Second), false, "(objectClass=*)", d.attributes(), nil))
	if err != nil {
		return nil, fmt.Errorf("%w: read user entry: %w", ErrUnavailable, err)
	}

	if len(res.Entries) != 1 {
		return nil, ErrInvalidCredentials
	}

	return res.Entries[0], nil
}

func (d Client) groups(conn tracedConn, user *goldap.Entry, base string) ([]string, error) {
	seen := map[string]bool{}

	var out []string

	add := func(name string) {
		if key := strings.ToLower(name); name != "" && !seen[key] {
			seen[key] = true

			out = append(out, name)
		}
	}

	for _, g := range d.Mappings {
		if g.Server != d.Server.Name {
			continue
		}

		switch g.strategy() {
		case StrategyStatic:
			scope := goldap.ScopeSingleLevel
			if g.SubTree {
				scope = goldap.ScopeWholeSubtree
			}

			filter := "(&" + g.filter() + "(" + g.MemberAttribute + "=" + goldap.EscapeFilter(user.DN) + "))"

			res, err := conn.Search(goldap.NewSearchRequest(under(g.BaseDN, base), scope, goldap.NeverDerefAliases,
				0, int(timeout/time.Second), false, filter, []string{
					g.NameAttribute,
				}, nil))
			if err != nil {
				if goldap.IsErrorWithCode(err, goldap.LDAPResultNoSuchObject) {
					continue
				}

				return nil, fmt.Errorf("%w: search groups %s: %w", ErrUnavailable, g.Name, err)
			}

			for _, e := range res.Entries {
				add(e.GetAttributeValue(g.NameAttribute))
			}
		case StrategyDynamic:
			for _, dn := range user.GetAttributeValues(g.MemberAttribute) {
				add(rdnValue(dn, g.NameAttribute))
			}
		case StrategyHierarchy:
			for _, name := range unitNames(user.DN, base, g.NameAttribute) {
				add(name)
			}
		}
	}

	return out, nil
}

func rdnValue(dn, attr string) string {
	parsed, err := goldap.ParseDN(dn)
	if err != nil || len(parsed.RDNs) == 0 {
		return ""
	}

	for _, a := range parsed.RDNs[0].Attributes {
		if strings.EqualFold(a.Type, attr) {
			return a.Value
		}
	}

	return ""
}

func unitNames(dn, base, attribute string) []string {
	if attribute == "" {
		attribute = "ou"
	}

	parsed, err := goldap.ParseDN(dn)
	if err != nil {
		return nil
	}

	baseDN, _ := goldap.ParseDN(base)

	var out []string

	for i, rdn := range parsed.RDNs {
		if i == 0 {
			continue
		}

		if baseDN != nil && len(parsed.RDNs)-i <= len(baseDN.RDNs) {
			break
		}

		for _, a := range rdn.Attributes {
			if strings.EqualFold(a.Type, attribute) {
				out = append(out, a.Value)
			}
		}
	}

	return out
}
