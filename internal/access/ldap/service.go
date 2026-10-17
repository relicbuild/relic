package ldap

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"uuid"

	"relic/internal/access/auth"
	"relic/internal/platform/database"
	"relic/internal/platform/sealing"
)

// ErrNotFound means no settings document has that key or name.
var ErrNotFound = errors.New("ldap: not found")

// ErrExists means a directory of that name is already configured.
var ErrExists = errors.New("ldap: already exists")

// Service stores LDAP settings and signs users in against them.
type Service struct {
	db  *database.DB
	box *sealing.Box
	tls *tls.Config
	log *slog.Logger
}

// NewService returns a Service.
func NewService(db *database.DB, box *sealing.Box, tlsConfig *tls.Config, log *slog.Logger) *Service {
	return &Service{
		db:  db,
		box: box,
		tls: tlsConfig,
		log: log,
	}
}

// passwordPlace binds a sealed manager password to its settings key, so a
// value copied to another row does not open.
func passwordPlace(key string) string { return "ldap_settings/" + key + "/manager_password" }

// Servers lists the LDAP servers, manager passwords opened.
func (s *Service) Servers(ctx context.Context) ([]Server, error) {
	rows, err := s.db.ListLdapServers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list ldap settings: %w", err)
	}

	out := make([]Server, 0, len(rows))
	for _, r := range rows {
		st, err := s.decode(r.Name, r.Config)
		if err != nil {
			return nil, err
		}

		out = append(out, st)
	}

	return out, nil
}

// Server returns one LDAP server, manager password opened.
func (s *Service) Server(ctx context.Context, name string) (Server, error) {
	r, err := s.db.GetLdapServer(ctx, name)
	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		return Server{}, ErrNotFound
	}

	if err != nil {
		return Server{}, fmt.Errorf("get ldap directory %s: %w", name, err)
	}

	return s.decode(r.Name, r.Config)
}

func (s *Service) decode(key string, raw []byte) (Server, error) {
	var st Server
	if err := json.Unmarshal(raw, &st); err != nil {
		return Server{}, fmt.Errorf("decode ldap settings %s: %w", key, err)
	}

	if pw := st.Search.ManagerPassword; sealing.SealedString(pw) {
		plain, err := s.box.OpenString(pw, passwordPlace(key))
		if err != nil {
			s.log.Warn("ldap manager password does not open; searching without it", "key", key, "error", err)

			plain = ""
		}

		st.Search.ManagerPassword = plain
	}

	return st, nil
}

// CreateLdapServer stores a new directory, refusing a name already configured.
func (s *Service) CreateLdapServer(ctx context.Context, st Server) error {
	return s.db.WithLock(ctx, "ldap-server:"+st.Name, func() error {
		_, err := s.Server(ctx, st.Name)
		if err == nil {
			return fmt.Errorf("%w: %s", ErrExists, st.Name)
		}

		if !errors.Is(err, ErrNotFound) {
			return err
		}

		return s.PutLdapServer(ctx, st)
	})
}

// PutLdapServer stores an LDAP server.
func (s *Service) PutLdapServer(ctx context.Context, st Server) error {
	if err := st.Validate(); err != nil {
		return err
	}

	if st.Search.ManagerPassword == "" {
		if old, err := s.Server(ctx, st.Name); err == nil && old.Search.ManagerDN == st.Search.ManagerDN {
			st.Search.ManagerPassword = old.Search.ManagerPassword
		}
	}

	if pw := st.Search.ManagerPassword; pw != "" && s.box.Enabled() {
		sealed, err := s.box.SealString(pw, passwordPlace(st.Name))
		if err != nil {
			return fmt.Errorf("seal ldap manager password: %w", err)
		}

		st.Search.ManagerPassword = sealed
	}

	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}

	if err := s.db.UpsertLdapServer(ctx, database.UpsertLdapServerParams{
		Name:   st.Name,
		Config: raw,
	}); err != nil {
		return fmt.Errorf("store ldap directory %s: %w", st.Name, err)
	}

	return nil
}

// DeleteLdapServer removes an LDAP server.
func (s *Service) DeleteLdapServer(ctx context.Context, name string) error {
	n, err := s.db.DeleteLdapServer(ctx, name)
	if err != nil {
		return fmt.Errorf("delete ldap directory %s: %w", name, err)
	}

	if n == 0 {
		return ErrNotFound
	}

	return nil
}

// GroupMappings lists the group mappings.
func (s *Service) GroupMappings(ctx context.Context) ([]GroupMapping, error) {
	rows, err := s.db.ListLdapGroupMappings(ctx)
	if err != nil {
		return nil, fmt.Errorf("list ldap group settings: %w", err)
	}

	out := make([]GroupMapping, 0, len(rows))
	for _, r := range rows {
		var g GroupMapping
		if err := json.Unmarshal(r.Config, &g); err != nil {
			return nil, fmt.Errorf("decode ldap group settings %s: %w", r.Name, err)
		}

		out = append(out, g)
	}

	return out, nil
}

// GroupMapping returns one group mapping.
func (s *Service) GroupMapping(ctx context.Context, name string) (GroupMapping, error) {
	r, err := s.db.GetLdapGroupMapping(ctx, name)
	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		return GroupMapping{}, ErrNotFound
	}

	if err != nil {
		return GroupMapping{}, fmt.Errorf("get ldap group settings %s: %w", name, err)
	}

	var g GroupMapping
	if err := json.Unmarshal(r.Config, &g); err != nil {
		return GroupMapping{}, fmt.Errorf("decode ldap group settings %s: %w", name, err)
	}

	return g, nil
}

// PutLdapGroupMapping stores an LDAP group mapping.
func (s *Service) PutLdapGroupMapping(ctx context.Context, g GroupMapping) error {
	if err := g.Validate(); err != nil {
		return err
	}

	raw, err := json.Marshal(g)
	if err != nil {
		return err
	}

	if err := s.db.UpsertLdapGroupMapping(
		ctx,
		database.UpsertLdapGroupMappingParams{
			Name:   g.Name,
			Config: raw,
		},
	); err != nil {
		return fmt.Errorf("store ldap group settings %s: %w", g.Name, err)
	}

	return nil
}

// DeleteLdapGroupMapping removes an LDAP group mapping.
func (s *Service) DeleteLdapGroupMapping(ctx context.Context, name string) error {
	n, err := s.db.DeleteLdapGroupMapping(ctx, name)
	if err != nil {
		return fmt.Errorf("delete ldap group settings %s: %w", name, err)
	}

	if n == 0 {
		return ErrNotFound
	}

	return nil
}

func (s *Service) clients(ctx context.Context) ([]Client, error) {
	all, err := s.Servers(ctx)
	if err != nil {
		return nil, err
	}

	groups, err := s.GroupMappings(ctx)
	if err != nil {
		return nil, err
	}

	var out []Client

	for _, st := range all {
		if st.Enabled {
			out = append(out, Client{
				Server:   st,
				Mappings: groups,
				TLS:      s.tls,
			})
		}
	}

	return out, nil
}

// Authenticate implements auth.ExternalAuthenticator: it tries each
// enabled directory in key order.
func (s *Service) Authenticate(ctx context.Context, username, password string) error {
	dirs, err := s.clients(ctx)
	if err != nil {
		return err
	}

	var unavailable error

	for _, d := range dirs {
		entry, err := d.Authenticate(ctx, username, password)
		switch {
		case errors.Is(err, ErrInvalidCredentials):
			s.log.DebugContext(
				ctx,
				"directory refused sign-in",
				"server",
				d.Server.Name,
				"user",
				username,
				"reason",
				err,
			)

			continue
		case err != nil:
			unavailable = errors.Join(unavailable, fmt.Errorf("%s: %w", d.Server.Name, err))
			continue
		}

		return s.provision(ctx, d.Server, username, entry)
	}

	if unavailable != nil {
		return unavailable
	}

	if len(dirs) == 0 {
		s.log.DebugContext(ctx, "no enabled LDAP server for sign-in", "user", username)
	}

	return auth.ErrBadCredentials
}

func (s *Service) provision(ctx context.Context, st Server, username string, e Entry) error {
	user, err := s.db.GetUserByUsername(ctx, username)
	switch {
	case errors.Is(database.NotFound(err), database.ErrNotFound):
		if !st.AutoCreateUser {
			s.log.DebugContext(
				ctx,
				"directory user refused: the server does not create users",
				"server",
				st.Name,
				"user",
				username,
			)

			return auth.ErrBadCredentials
		}

		if err := s.db.CreateLdapUser(
			ctx,
			database.CreateLdapUserParams{
				ID:       uuid.New(),
				Username: username,
				Email:    e.Email,
			},
		); err != nil {
			return fmt.Errorf("create ldap user %s: %w", username, err)
		}
	case err != nil:
		return fmt.Errorf("look up user %s: %w", username, err)
	case user.Realm != auth.RealmLDAP:
		s.log.DebugContext(ctx, "directory user refused: a local account has the name", "user", username)
		return auth.ErrBadCredentials // a local account is never signed in by the directory
	case e.Email != "":
		if err := s.db.SetUserEmail(ctx, database.SetUserEmailParams{
			Username: username,
			Email:    e.Email,
		}); err != nil {
			return fmt.Errorf("update email of %s: %w", username, err)
		}
	}

	names := make([]string, 0, len(e.Groups))
	for _, g := range e.Groups {
		names = append(names, strings.ToLower(g))
	}

	if err := s.db.WithTx(ctx, func(q database.Querier) error {
		return q.ReplaceLdapMemberships(
			ctx,
			database.ReplaceLdapMembershipsParams{
				Username:   username,
				GroupNames: names,
			},
		)
	}); err != nil {
		return fmt.Errorf("sync ldap groups of %s: %w", username, err)
	}

	return nil
}

// Test tries settings (not necessarily stored) with a user's credentials,
// for the administration UI.
func (s *Service) Test(ctx context.Context, st Server, username, password string) (Entry, error) {
	if err := st.Validate(); err != nil {
		return Entry{}, err
	}

	if st.Search.ManagerPassword == "" {
		if old, err := s.Server(ctx, st.Name); err == nil && old.Search.ManagerDN == st.Search.ManagerDN {
			st.Search.ManagerPassword = old.Search.ManagerPassword
		}
	}

	groups, err := s.GroupMappings(ctx)
	if err != nil {
		return Entry{}, err
	}

	return Client{
		Server:   st,
		Mappings: groups,
		TLS:      s.tls,
	}.Authenticate(ctx, username, password)
}

// SealStored seals manager passwords stored before a secrets key was set.
func (s *Service) SealStored(ctx context.Context) error {
	if !s.box.Enabled() {
		return nil
	}

	rows, err := s.db.ListLdapServers(ctx)
	if err != nil {
		return fmt.Errorf("list ldap settings: %w", err)
	}

	var errs []error

	for _, r := range rows {
		var raw Server
		if err := json.Unmarshal(r.Config, &raw); err != nil {
			errs = append(errs, fmt.Errorf("decode ldap directory %s: %w", r.Name, err))
			continue
		}

		if raw.Search.ManagerPassword == "" || sealing.SealedString(raw.Search.ManagerPassword) {
			continue
		}

		if err := s.PutLdapServer(ctx, raw); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}
