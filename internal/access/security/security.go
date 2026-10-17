// Package security manages users, groups and permissions, and compiles
// permissions for authorization.
package security

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
	"uuid"

	"relic/internal/access/auth"
	"relic/internal/platform/database"
)

// Errors mapped by the API layer.
var (
	ErrNotFound = errors.New("not found")
	ErrExists   = errors.New("already exists")
	ErrInvalid  = errors.New("invalid request")
)

const permissionsTTL = 5 * time.Second

// Service implements user, group and permission management.
type Service struct {
	db  *database.DB
	log *slog.Logger

	adminOnly []string
	// projectPermissions adds the projects' roles to the stored permissions.
	projectPermissions func(context.Context) ([]auth.Permission, error)
	removalHooks       []PrincipalRemovalHook

	mu       sync.Mutex
	perms    auth.Permissions
	loadedAt time.Time
}

// NewService returns a Service.
func NewService(db *database.DB, log *slog.Logger, adminOnly ...string) *Service {
	return &Service{
		db:        db,
		log:       log,
		adminOnly: adminOnly,
	}
}

// User is a user as the security API exposes it.
type User struct {
	Name   string
	Email  string
	Admin  bool
	Viewer bool
	// Disabled users cannot sign in; PutUser leaves it as it is (SetDisabled).
	Disabled bool
	Groups   []string
	// Extensions keeps settings only an API adapter understands, by adapter
	// name.
	Extensions map[string]json.RawMessage
	Password   string
	Realm      string
}

// PutUser creates or replaces a user (PUT /api/security/users/{name}).
func (s *Service) PutUser(ctx context.Context, u User) (created bool, err error) {
	return s.putUser(ctx, u, userWrite{})
}

// CreateUser stores a new user; ErrExists if the name is taken.
func (s *Service) CreateUser(ctx context.Context, u User) error {
	_, err := s.putUser(ctx, u, userWrite{
		createOnly: true,
	})

	return err
}

// ReplaceUser is PutUser that also applies u.Disabled, in the same
// transaction.
func (s *Service) ReplaceUser(ctx context.Context, u User) (created bool, err error) {
	return s.putUser(ctx, u, userWrite{
		withDisabled: true,
	})
}

type userWrite struct {
	createOnly   bool
	withDisabled bool
}

func (s *Service) putUser(ctx context.Context, u User, w userWrite) (created bool, err error) {
	if u.Name == "" {
		return false, fmt.Errorf("%w: name is required", ErrInvalid)
	}

	existing, err := s.db.GetUserByUsername(ctx, u.Name)

	exists := err == nil
	if err != nil && !errors.Is(database.NotFound(err), database.ErrNotFound) {
		return false, fmt.Errorf("get user: %w", err)
	}

	if exists && w.createOnly {
		return false, fmt.Errorf("%w: user %q", ErrExists, u.Name)
	}

	hash := existing.PasswordHash

	switch {
	case u.Password != "":
		if hash, err = auth.HashPassword(u.Password); err != nil {
			return false, err
		}
	case !exists:
		return false, fmt.Errorf("%w: password is required", ErrInvalid)
	}

	extensions, err := json.Marshal(nonNil(u.Extensions))
	if err != nil {
		return false, err
	}

	disabled := existing.Disabled
	if w.withDisabled {
		disabled = u.Disabled
	}

	removesAdministrator := exists && existing.Admin && !existing.Disabled && (!u.Admin || disabled)

	err = s.withAdministratorKept(ctx, removesAdministrator, func() error {
		return s.db.WithTx(ctx, func(q database.Querier) error {
			created, err = q.UpsertUser(ctx, database.UpsertUserParams{
				ID:           uuid.NewV7(),
				Username:     u.Name,
				Email:        u.Email,
				PasswordHash: hash,
				Admin:        u.Admin,
				Viewer:       u.Viewer,
				Extensions:   extensions,
			})
			if err != nil {
				return fmt.Errorf("save user: %w", err)
			}

			if w.createOnly && !created {
				return fmt.Errorf("%w: user %q", ErrExists, u.Name)
			}

			if w.withDisabled {
				if err := q.SetUserDisabled(ctx, database.SetUserDisabledParams{
					Username: u.Name,
					Disabled: u.Disabled,
				}); err != nil {
					return fmt.Errorf("save user: %w", err)
				}
			}

			groups := u.Groups
			if groups == nil && created {
				if groups, err = q.DefaultGroups(ctx); err != nil {
					return err
				}
			}

			if groups == nil {
				return nil
			}

			return setUserGroups(ctx, q, u.Name, groups)
		})
	})
	s.invalidate()

	return created, err
}

func setUserGroups(ctx context.Context, q database.Querier, username string, groups []string) error {
	if err := q.ClearUserGroups(ctx, username); err != nil {
		return err
	}

	for _, g := range groups {
		if _, err := q.GetGroup(ctx, g); err != nil {
			if errors.Is(database.NotFound(err), database.ErrNotFound) {
				return fmt.Errorf("%w: group %q", ErrNotFound, g)
			}

			return err
		}

		if err := q.AddGroupMembers(
			ctx,
			database.AddGroupMembersParams{
				GroupName: g,
				Usernames: []string{
					username,
				},
			},
		); err != nil {
			return err
		}
	}

	return nil
}

// GetUser returns a user with its groups.
func (s *Service) GetUser(ctx context.Context, name string) (User, error) {
	row, err := s.db.GetUserByUsername(ctx, name)
	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		return User{}, fmt.Errorf("%w: user %q", ErrNotFound, name)
	}

	if err != nil {
		return User{}, err
	}

	groups, _, err := s.Groups(ctx, name)
	if err != nil {
		return User{}, err
	}

	var extensions map[string]json.RawMessage
	if err := json.Unmarshal(row.Extensions, &extensions); err != nil {
		s.log.ErrorContext(ctx, "user extensions do not decode", "user", name, "error", err)
	}

	return User{
		Name:       row.Username,
		Email:      row.Email,
		Admin:      row.Admin,
		Viewer:     row.Viewer,
		Disabled:   row.Disabled,
		Groups:     groups,
		Extensions: extensions,
		Realm:      row.Realm,
	}, nil
}

// ListUsers returns every user name.
func (s *Service) ListUsers(ctx context.Context) ([]string, error) {
	rows, err := s.db.ListUsers(ctx)
	if err != nil {
		return nil, err
	}

	names := make([]string, len(rows))
	for i, r := range rows {
		names[i] = r.Username
	}

	return names, nil
}

// UserRealm is a user's name and where their credentials are checked.
type UserRealm struct {
	Name, Realm string
}

// ListUserRealms lists users with their realms, by name.
func (s *Service) ListUserRealms(ctx context.Context) ([]UserRealm, error) {
	rows, err := s.db.ListUsers(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]UserRealm, len(rows))
	for i, r := range rows {
		out[i] = UserRealm{
			Name:  r.Username,
			Realm: r.Realm,
		}
	}

	return out, nil
}

const administratorsLock = "security:administrators"

// DeleteUser removes a user, their grants and what the removal hooks drop.
func (s *Service) DeleteUser(ctx context.Context, name string) error {
	u, err := s.db.GetUserByUsername(ctx, name)
	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		return fmt.Errorf("%w: user %q", ErrNotFound, name)
	}

	if err != nil {
		return err
	}

	remove := func() error {
		return s.db.WithTx(ctx, func(q database.Querier) error {
			if err := q.RevokeUserAccessTokens(ctx, name); err != nil {
				return fmt.Errorf("revoke the access tokens of %q: %w", name, err)
			}

			if _, err := q.DeleteUser(ctx, name); err != nil {
				return err
			}

			return s.removePrincipal(ctx, q, name, false)
		})
	}

	if err := s.withAdministratorKept(ctx, u.Admin && !u.Disabled, remove); err != nil {
		return err
	}

	s.invalidate()

	return nil
}

// withAdministratorKept runs change, under the administrators lock when it takes away an enabled administrator,
// and refuses it when that administrator is the last one.
func (s *Service) withAdministratorKept(ctx context.Context, removesAdministrator bool, change func() error) error {
	if !removesAdministrator {
		return change()
	}

	return s.db.WithLock(ctx, administratorsLock, func() error {
		users, err := s.db.ListUsers(ctx)
		if err != nil {
			return fmt.Errorf("list users: %w", err)
		}

		enabled := 0

		for _, u := range users {
			if u.Admin && !u.Disabled {
				enabled++
			}
		}

		if enabled <= 1 {
			return fmt.Errorf("%w: the last enabled administrator cannot be deleted, disabled or demoted", ErrInvalid)
		}

		return change()
	})
}

// SetDisabled turns a user's sign-ins off or back on.
func (s *Service) SetDisabled(ctx context.Context, name string, disabled bool) error {
	u, err := s.db.GetUserByUsername(ctx, name)
	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		return fmt.Errorf("%w: user %q", ErrNotFound, name)
	}

	if err != nil {
		return err
	}

	return s.withAdministratorKept(ctx, u.Admin && !u.Disabled && disabled, func() error {
		return s.db.SetUserDisabled(ctx, database.SetUserDisabledParams{
			Username: name,
			Disabled: disabled,
		})
	})
}

// Groups returns a user's group names and whether any grants admin.
func (s *Service) Groups(ctx context.Context, username string) ([]string, auth.Privileges, error) {
	rows, err := s.db.UserGroups(ctx, username)
	if err != nil {
		return nil, auth.Privileges{}, fmt.Errorf("user groups: %w", err)
	}

	var granted auth.Privileges

	names := make([]string, len(rows))
	for i, r := range rows {
		names[i] = r.Name
		granted.Admin = granted.Admin || r.Admin
		granted.Viewer = granted.Viewer || r.Viewer
	}

	return names, granted, nil
}

// Group is a group as the security API exposes it.
type Group struct {
	Name        string
	Description string
	// DefaultForNewUsers adds users created without groups to the group.
	DefaultForNewUsers bool
	// Admin makes the group's members administrators.
	Admin bool
	// Viewer makes the group's members viewers.
	Viewer  bool
	Members []string
}

// PutGroup creates or replaces a group. members, when non-nil, replaces
// the membership.
func (s *Service) PutGroup(ctx context.Context, g Group, members []string) (created bool, err error) {
	return s.putGroup(ctx, g, members, false)
}

// CreateGroup stores a new group; ErrExists if the name is taken.
func (s *Service) CreateGroup(ctx context.Context, g Group, members []string) error {
	_, err := s.putGroup(ctx, g, members, true)

	return err
}

func (s *Service) putGroup(ctx context.Context, g Group, members []string, createOnly bool) (created bool, err error) {
	if g.Name == "" {
		return false, fmt.Errorf("%w: name is required", ErrInvalid)
	}

	err = s.db.WithTx(ctx, func(q database.Querier) error {
		created, err = q.UpsertGroup(
			ctx,
			database.UpsertGroupParams{
				Name:               g.Name,
				Description:        g.Description,
				DefaultForNewUsers: g.DefaultForNewUsers,
				Admin:              g.Admin,
				Viewer:             g.Viewer,
			},
		)
		if err != nil {
			return err
		}

		if createOnly && !created {
			return fmt.Errorf("%w: group %q", ErrExists, g.Name)
		}

		if members == nil {
			return nil
		}

		if err := q.ClearGroupMembers(ctx, g.Name); err != nil {
			return err
		}

		return addMembers(ctx, q, g.Name, members)
	})
	s.invalidate()

	return created, err
}

// AddMembers adds users to an existing group, keeping current members.
func (s *Service) AddMembers(ctx context.Context, group string, users []string) error {
	err := s.db.WithTx(ctx, func(q database.Querier) error {
		if _, err := q.GetGroup(ctx, group); err != nil {
			if errors.Is(database.NotFound(err), database.ErrNotFound) {
				return fmt.Errorf("%w: group %q", ErrNotFound, group)
			}

			return err
		}

		return addMembers(ctx, q, group, users)
	})
	s.invalidate()

	return err
}

func addMembers(ctx context.Context, q database.Querier, group string, users []string) error {
	for _, u := range users {
		if _, err := q.GetUserByUsername(ctx, u); err != nil {
			if errors.Is(database.NotFound(err), database.ErrNotFound) {
				return fmt.Errorf("%w: user %q", ErrNotFound, u)
			}

			return err
		}
	}

	return q.AddGroupMembers(ctx, database.AddGroupMembersParams{
		GroupName: group,
		Usernames: users,
	})
}

// GetGroup returns a group, with members when includeMembers is set.
func (s *Service) GetGroup(ctx context.Context, name string, includeMembers bool) (Group, error) {
	row, err := s.db.GetGroup(ctx, name)
	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		return Group{}, fmt.Errorf("%w: group %q", ErrNotFound, name)
	}

	if err != nil {
		return Group{}, err
	}

	g := Group{
		Name:               row.Name,
		Description:        row.Description,
		DefaultForNewUsers: row.DefaultForNewUsers,
		Admin:              row.Admin,
		Viewer:             row.Viewer,
	}
	if includeMembers {
		if g.Members, err = s.db.GroupMembers(ctx, name); err != nil {
			return Group{}, err
		}
	}

	return g, nil
}

// ListGroups returns every group name.
func (s *Service) ListGroups(ctx context.Context) ([]string, error) {
	rows, err := s.db.ListGroups(ctx)
	if err != nil {
		return nil, err
	}

	names := make([]string, len(rows))
	for i, r := range rows {
		names[i] = r.Name
	}

	return names, nil
}

// DeleteGroup removes a group, its grants and what the removal hooks drop.
func (s *Service) DeleteGroup(ctx context.Context, name string) error {
	err := s.db.WithTx(ctx, func(q database.Querier) error {
		n, err := q.DeleteGroup(ctx, name)
		if err != nil {
			return err
		}

		if n == 0 {
			return fmt.Errorf("%w: group %q", ErrNotFound, name)
		}

		return s.removePrincipal(ctx, q, name, true)
	})
	if err != nil {
		return err
	}

	s.invalidate()

	return nil
}

// Permissions returns the compiled permissions, reloaded at most every
// permissionsTTL.
func (s *Service) Permissions(ctx context.Context) (auth.Permissions, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if time.Since(s.loadedAt) < permissionsTTL {
		return s.perms, nil
	}

	rows, err := s.db.ListPermissions(ctx)
	if err != nil {
		return auth.Permissions{}, fmt.Errorf("load permissions: %w", err)
	}

	perms := auth.Permissions{
		AdminOnly: s.adminOnly,
	}

	for _, r := range rows {
		pt, err := decodePermission(r.Name, r.Config)
		if err != nil {
			s.log.ErrorContext(ctx, "skipping unreadable permission", "name", r.Name, "error", err)
			continue
		}

		perms.All = append(perms.All, pt.compile())
	}

	if s.projectPermissions != nil {
		projects, err := s.projectPermissions(ctx)
		if err != nil {
			return auth.Permissions{}, fmt.Errorf("load project permissions: %w", err)
		}

		perms.All = append(perms.All, projects...)
	}

	s.perms, s.loadedAt = perms, time.Now()

	return perms, nil
}

// SetProjectPermissions adds the permissions fn returns, the projects'
// roles, to the stored ones.
func (s *Service) SetProjectPermissions(fn func(context.Context) ([]auth.Permission, error)) {
	s.projectPermissions = fn
}

// PrincipalRemovalHook drops what refers to a user, or a group, by name, in
// the transaction that deletes it.
type PrincipalRemovalHook func(ctx context.Context, q database.Querier, name string, group bool) error

// RegisterPrincipalRemovalHook runs hook whenever a user or a group is
// deleted.
func (s *Service) RegisterPrincipalRemovalHook(hook PrincipalRemovalHook) {
	s.removalHooks = append(s.removalHooks, hook)
}

func (s *Service) removePrincipal(ctx context.Context, q database.Querier, name string, group bool) error {
	principal := Grant{
		User: name,
	}
	if group {
		principal = Grant{
			Group: name,
		}
	}

	if err := removeGrants(ctx, q, principal); err != nil {
		return err
	}

	for _, hook := range s.removalHooks {
		if err := hook(ctx, q, name, group); err != nil {
			return err
		}
	}

	return nil
}

// Invalidate makes the next Permissions call reload them.
func (s *Service) Invalidate() { s.invalidate() }

func (s *Service) invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.loadedAt = time.Time{}
}

func nonNil(m map[string]json.RawMessage) map[string]json.RawMessage {
	if m == nil {
		return map[string]json.RawMessage{}
	}

	return m
}

func splitList(items []string) []string {
	var out []string
	for _, s := range items {
		if s = strings.TrimSpace(s); s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}

	return out
}
