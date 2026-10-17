package sso

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"uuid"

	"relic/internal/access/security"
	"relic/internal/platform/database"
)

// Directory errors.
var (
	ErrEntryNotFound = errors.New("not found")
	ErrInvalidValue  = errors.New("invalid value")
	ErrConflict      = errors.New("already exists")
	ErrImmutable     = errors.New("cannot be changed")
)

// DirectoryUser is a single sign-on user as an identity provider's
// provisioning (SCIM) sees it.
type DirectoryUser struct {
	ID       string
	UserName string
	Email    string
	Active   bool
	Created  time.Time
}

// DirectoryGroup is a group and the members an identity provider manages.
type DirectoryGroup struct {
	Name    string
	Created time.Time
	Members []DirectoryUser
}

// Directory provisions single sign-on users and their group memberships
// for an identity provider. It sees only users of the sso realm, and only
// the memberships it manages.
type Directory struct {
	db       *database.DB
	security *security.Service
}

// NewDirectory returns a Directory; sec creates and deletes groups.
func NewDirectory(db *database.DB, sec *security.Service) *Directory {
	return &Directory{
		db:       db,
		security: sec,
	}
}

func directoryUser(u database.User) DirectoryUser {
	return DirectoryUser{
		ID:       u.ID.String(),
		UserName: u.Username,
		Email:    u.Email,
		Active:   !u.Disabled,
		Created:  u.Created,
	}
}

func notFound(what, id string) error {
	return fmt.Errorf("%w: %s %s", ErrEntryNotFound, what, id)
}

// Users returns a page of users, those named userName when it is set, and
// how many there are.
func (d *Directory) Users(ctx context.Context, userName string, skip, limit int) ([]DirectoryUser, int64, error) {
	total, err := d.db.CountSsoUsers(ctx, userName)
	if err != nil {
		return nil, 0, fmt.Errorf("count users: %w", err)
	}

	rows, err := d.db.ListSsoUsers(ctx, database.ListSsoUsersParams{
		Username: userName,
		Skip:     int32(skip),  //nolint:gosec // bounded by the caller
		MaxRows:  int32(limit), //nolint:gosec // bounded by the caller
	})
	if err != nil {
		return nil, 0, fmt.Errorf("list users: %w", err)
	}

	out := make([]DirectoryUser, len(rows))
	for i, r := range rows {
		out[i] = directoryUser(r)
	}

	return out, total, nil
}

func (d *Directory) row(ctx context.Context, id string) (database.User, error) {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return database.User{}, notFound("user", id)
	}

	u, err := d.db.GetSsoUser(ctx, parsed)
	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		return database.User{}, notFound("user", id)
	}

	if err != nil {
		return database.User{}, fmt.Errorf("get user %s: %w", id, err)
	}

	return u, nil
}

// User returns the user with this ID.
func (d *Directory) User(ctx context.Context, id string) (DirectoryUser, error) {
	u, err := d.row(ctx, id)
	if err != nil {
		return DirectoryUser{}, err
	}

	return directoryUser(u), nil
}

// CreateUser creates a single sign-on user. A user of any realm with the
// same name is a conflict.
func (d *Directory) CreateUser(ctx context.Context, u DirectoryUser) (DirectoryUser, error) {
	if !security.ValidUsername(u.UserName) {
		return DirectoryUser{}, fmt.Errorf("%w: userName %q is not a valid Relic username", ErrInvalidValue, u.UserName)
	}

	if _, err := d.db.GetUserByUsername(ctx, u.UserName); err == nil {
		return DirectoryUser{}, fmt.Errorf("%w: user %s", ErrConflict, u.UserName)
	} else if !errors.Is(database.NotFound(err), database.ErrNotFound) {
		return DirectoryUser{}, err
	}

	if err := createUser(
		ctx,
		d.db,
		identity{
			username: u.UserName,
			email:    u.Email,
		},
		"",
		!u.Active,
	); err != nil {
		return DirectoryUser{}, err
	}

	created, err := d.db.GetUserByUsername(ctx, u.UserName)
	if err != nil {
		return DirectoryUser{}, err
	}

	return directoryUser(created), nil
}

// UpdateUser applies u's email and active state to the user with u.ID. The
// user name cannot change.
func (d *Directory) UpdateUser(ctx context.Context, u DirectoryUser) (DirectoryUser, error) {
	current, err := d.row(ctx, u.ID)
	if err != nil {
		return DirectoryUser{}, err
	}

	if u.UserName != "" && !strings.EqualFold(u.UserName, current.Username) {
		return DirectoryUser{}, fmt.Errorf("%w: the user name of %s", ErrImmutable, current.Username)
	}

	if u.Email != current.Email {
		if err := d.db.SetUserEmail(ctx, database.SetUserEmailParams{
			Username: current.Username,
			Email:    u.Email,
		}); err != nil {
			return DirectoryUser{}, err
		}
	}

	if u.Active == current.Disabled {
		if err := d.db.SetUserDisabled(ctx, database.SetUserDisabledParams{
			Username: current.Username,
			Disabled: !u.Active,
		}); err != nil {
			return DirectoryUser{}, err
		}
	}

	return d.User(ctx, u.ID)
}

// PatchUser writes only the email and active state that differ between
// before and after, so a concurrent change to the other is kept.
func (d *Directory) PatchUser(ctx context.Context, before, after DirectoryUser) (DirectoryUser, error) {
	if after.UserName != "" && !strings.EqualFold(after.UserName, before.UserName) {
		return DirectoryUser{}, fmt.Errorf("%w: the user name of %s", ErrImmutable, before.UserName)
	}

	if after.Email != before.Email {
		if err := d.db.SetUserEmail(ctx, database.SetUserEmailParams{
			Username: before.UserName,
			Email:    after.Email,
		}); err != nil {
			return DirectoryUser{}, err
		}
	}

	if after.Active != before.Active {
		if err := d.db.SetUserDisabled(ctx, database.SetUserDisabledParams{
			Username: before.UserName,
			Disabled: !after.Active,
		}); err != nil {
			return DirectoryUser{}, err
		}
	}

	return d.User(ctx, before.ID)
}

// DeleteUser deletes the user with this ID.
func (d *Directory) DeleteUser(ctx context.Context, id string) error {
	u, err := d.row(ctx, id)
	if err != nil {
		return err
	}

	return d.security.DeleteUser(ctx, u.Username)
}

// Groups returns a page of groups, the one named name when it is set, and
// how many there are.
func (d *Directory) Groups(ctx context.Context, name string, skip, limit int) ([]DirectoryGroup, int64, error) {
	total, err := d.db.CountGroups(ctx, name)
	if err != nil {
		return nil, 0, fmt.Errorf("count groups: %w", err)
	}

	rows, err := d.db.ListGroupsPage(ctx, database.ListGroupsPageParams{
		Name:    name,
		Skip:    int32(skip),  //nolint:gosec // bounded by the caller
		MaxRows: int32(limit), //nolint:gosec // bounded by the caller
	})
	if err != nil {
		return nil, 0, fmt.Errorf("list groups: %w", err)
	}

	out := make([]DirectoryGroup, 0, len(rows))
	for _, r := range rows {
		g, err := d.members(ctx, r)
		if err != nil {
			return nil, 0, err
		}

		out = append(out, g)
	}

	return out, total, nil
}

func (d *Directory) members(ctx context.Context, g database.Group) (DirectoryGroup, error) {
	rows, err := d.db.SsoGroupMembers(ctx, g.Name)
	if err != nil {
		return DirectoryGroup{}, fmt.Errorf("members of %s: %w", g.Name, err)
	}

	out := DirectoryGroup{
		Name:    g.Name,
		Created: g.Created,
		Members: make([]DirectoryUser, len(rows)),
	}
	for i, r := range rows {
		out.Members[i] = DirectoryUser{
			ID:       r.ID.String(),
			UserName: r.Username,
		}
	}

	return out, nil
}

// Group returns a group and the members the identity provider manages.
func (d *Directory) Group(ctx context.Context, name string) (DirectoryGroup, error) {
	g, err := d.db.GetGroup(ctx, name)
	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		return DirectoryGroup{}, notFound("group", name)
	}

	if err != nil {
		return DirectoryGroup{}, err
	}

	return d.members(ctx, g)
}

// CreateGroup creates a group with these member IDs.
func (d *Directory) CreateGroup(ctx context.Context, name string, memberIDs []string) (DirectoryGroup, error) {
	members, err := d.usernames(ctx, memberIDs)
	if err != nil {
		return DirectoryGroup{}, err
	}

	err = d.security.CreateGroup(ctx, security.Group{
		Name: name,
	}, nil)
	if errors.Is(err, security.ErrExists) {
		return DirectoryGroup{}, fmt.Errorf("%w: group %s", ErrConflict, name)
	}

	if err != nil {
		return DirectoryGroup{}, err
	}

	if err := d.writeMembers(ctx, name, members, nil, true); err != nil {
		if derr := d.security.DeleteGroup(context.WithoutCancel(ctx), name); derr != nil {
			return DirectoryGroup{}, errors.Join(err, fmt.Errorf("remove the group whose members failed: %w", derr))
		}

		return DirectoryGroup{}, err
	}

	return d.Group(ctx, name)
}

// ChangeMembers adds and removes the members it manages; with replace, the
// added ones become its only members.
func (d *Directory) ChangeMembers(ctx context.Context, group string, add, remove []string, replace bool) error {
	if err := d.checkUnprivileged(ctx, group); err != nil {
		return err
	}

	adding, err := d.usernames(ctx, add)
	if err != nil {
		return err
	}

	removing, err := d.usernames(ctx, remove)
	if err != nil {
		return err
	}

	return d.writeMembers(ctx, group, adding, removing, replace)
}

func (d *Directory) writeMembers(ctx context.Context, group string, adding, removing []string, replace bool) error {
	return d.db.WithTx(ctx, func(q database.Querier) error {
		if replace {
			if err := q.ClearSsoGroupMembers(ctx, group); err != nil {
				return err
			}
		}

		for _, u := range removing {
			if err := q.RemoveSsoGroupMember(ctx, database.RemoveSsoGroupMemberParams{
				GroupName: group,
				Username:  u,
			}); err != nil {
				return err
			}
		}

		for _, u := range adding {
			if err := q.AddSsoGroupMember(ctx, database.AddSsoGroupMemberParams{
				GroupName: group,
				Username:  u,
			}); err != nil {
				return err
			}
		}

		return nil
	})
}

func (d *Directory) usernames(ctx context.Context, ids []string) ([]string, error) {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		u, err := d.row(ctx, id)
		if errors.Is(err, ErrEntryNotFound) {
			return nil, fmt.Errorf("%w: member %s is not a single sign-on user", ErrInvalidValue, id)
		}

		if err != nil {
			return nil, err
		}

		out = append(out, u.Username)
	}

	return out, nil
}

func (d *Directory) checkUnprivileged(ctx context.Context, name string) error {
	g, err := d.db.GetGroup(ctx, name)
	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		return notFound("group", name)
	}

	if err != nil {
		return err
	}

	if g.Admin || g.Viewer {
		return fmt.Errorf("%w: group %s grants administrator or viewer rights; change it in Relic", ErrImmutable, name)
	}

	return nil
}

// DeleteGroup deletes a group that grants no administrator or viewer rights
// and has only members the identity provider manages.
func (d *Directory) DeleteGroup(ctx context.Context, name string) error {
	if err := d.checkUnprivileged(ctx, name); err != nil {
		return err
	}

	all, err := d.db.GroupMembers(ctx, name)
	if err != nil {
		return fmt.Errorf("members of %s: %w", name, err)
	}

	managed, err := d.db.SsoGroupMembers(ctx, name)
	if err != nil {
		return fmt.Errorf("members of %s: %w", name, err)
	}

	if len(all) > len(managed) {
		return fmt.Errorf("%w: group %s has members Relic manages; delete it in Relic", ErrImmutable, name)
	}

	err = d.security.DeleteGroup(ctx, name)
	if errors.Is(err, security.ErrNotFound) {
		return notFound("group", name)
	}

	return err
}
