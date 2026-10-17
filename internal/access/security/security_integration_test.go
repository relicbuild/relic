//go:build integration

package security_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"relic/internal/access/auth"
	"relic/internal/access/project"
	"relic/internal/access/security"
	"relic/internal/platform/database"
	"relic/internal/platform/database/databasetest"
)

func TestMain(m *testing.M) { databasetest.Main(m) }

func TestDeleteUserRevokesTheirTokens(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := databasetest.NewDB(t)

	if err := db.CreateUser(ctx, database.CreateUserParams{
		ID:           uuid.NewV7(),
		Username:     "alice",
		PasswordHash: "x",
	}); err != nil {
		t.Fatal(err)
	}

	refresh := "refresh-hash"
	token := uuid.NewV7()

	if err := db.InsertAccessToken(ctx, database.InsertAccessTokenParams{
		ID:               token,
		Username:         "alice",
		ScopeKind:        "user",
		ScopeGroups:      []string{},
		RefreshTokenHash: &refresh,
	}); err != nil {
		t.Fatal(err)
	}

	if err := security.NewService(db, slog.New(slog.DiscardHandler)).DeleteUser(ctx, "alice"); err != nil {
		t.Fatal(err)
	}

	got, err := db.GetAccessToken(ctx, token)
	if err != nil {
		t.Fatal(err)
	}

	if !got.Revoked || got.RefreshTokenHash != nil {
		t.Errorf(
			"token of the deleted user = revoked %v, refresh hash %v; want revoked without one",
			got.Revoked,
			got.RefreshTokenHash,
		)
	}
}

func TestDeleteUserWaitsForOtherAdministratorChanges(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := databasetest.NewDB(t)

	for _, name := range []string{
		"first-admin",
		"second-admin",
	} {
		if err := db.CreateUser(ctx, database.CreateUserParams{
			ID:           uuid.NewV7(),
			Username:     name,
			PasswordHash: "x",
			Admin:        true,
		}); err != nil {
			t.Fatal(err)
		}
	}

	held := make(chan struct{})
	unlock := make(chan struct{})
	lockErr := make(chan error, 1)

	go func() {
		lockErr <- db.WithLock(ctx, "security:administrators", func() error {
			close(held)
			<-unlock

			return nil
		})
	}()

	<-held

	deleted := make(chan error, 1)

	go func() {
		deleted <- security.NewService(db, slog.New(slog.DiscardHandler)).DeleteUser(ctx, "first-admin")
	}()

	select {
	case err := <-deleted:
		t.Errorf("DeleteUser of an administrator went ahead while the administrators were locked (err %v)", err)
		close(unlock)
	case <-time.After(2 * time.Second):
		close(unlock)

		if err := <-deleted; err != nil {
			t.Fatal(err)
		}
	}

	if err := <-lockErr; err != nil {
		t.Fatal(err)
	}
}

func TestDeletingAPrincipalRemovesItsGrants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		remove      func(context.Context, *security.Service) error
		wantUsers   []string
		wantGroups  []string
		wantMembers []project.Member
	}{
		{
			name: "user",
			remove: func(ctx context.Context, s *security.Service) error {
				return s.DeleteUser(ctx, "temp")
			},
			wantUsers: []string{
				"kept",
			},
			wantGroups: []string{
				"temp",
				"developers",
			},
			wantMembers: []project.Member{
				{
					Name: "kept",
					Role: project.RoleViewer,
				},
				{
					Name:  "temp",
					Group: true,
					Role:  project.RoleAdmin,
				},
				{
					Name:  "developers",
					Group: true,
					Role:  project.RoleDeveloper,
				},
			},
		},
		{
			name: "group",
			remove: func(ctx context.Context, s *security.Service) error {
				return s.DeleteGroup(ctx, "temp")
			},
			wantUsers: []string{
				"temp",
				"kept",
			},
			wantGroups: []string{
				"developers",
			},
			wantMembers: []project.Member{
				{
					Name: "temp",
					Role: project.RoleAdmin,
				},
				{
					Name: "kept",
					Role: project.RoleViewer,
				},
				{
					Name:  "developers",
					Group: true,
					Role:  project.RoleDeveloper,
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			db := databasetest.NewDB(t)
			s := security.NewService(db, slog.New(slog.DiscardHandler))
			projects := project.New(db)
			s.RegisterPrincipalRemovalHook(projects.RemoveMemberships)

			for _, name := range []string{
				"temp",
				"kept",
			} {
				if err := db.CreateUser(ctx, database.CreateUserParams{
					ID:           uuid.NewV7(),
					Username:     name,
					PasswordHash: "x",
				}); err != nil {
					t.Fatal(err)
				}
			}

			for _, name := range []string{
				"temp",
				"developers",
			} {
				if err := s.CreateGroup(
					ctx,
					security.Group{
						Name: name,
					},
					nil,
				); err != nil {
					t.Fatal(err)
				}
			}

			read := []auth.Action{
				auth.ActionRead,
			}
			grants := []security.Grant{
				{
					User:    "temp",
					Actions: read,
				},
				{
					User:    "kept",
					Actions: read,
				},
				{
					Group:   "temp",
					Actions: read,
				},
				{
					Group:   "developers",
					Actions: read,
				},
			}

			if err := s.CreatePermission(ctx, security.Permission{
				Name: "libs",
				Rules: []security.Rule{
					{
						Resource: security.ResourceRepository,
						Grants:   grants,
					},
					{
						Resource: security.ResourceBuild,
						Grants:   grants,
					},
				},
			}); err != nil {
				t.Fatal(err)
			}

			if _, err := projects.Put(ctx, project.Project{
				Key: "apps",
				Members: []project.Member{
					{
						Name: "temp",
						Role: project.RoleAdmin,
					},
					{
						Name: "kept",
						Role: project.RoleViewer,
					},
					{
						Name:  "temp",
						Group: true,
						Role:  project.RoleAdmin,
					},
					{
						Name:  "developers",
						Group: true,
						Role:  project.RoleDeveloper,
					},
				},
			}); err != nil {
				t.Fatal(err)
			}

			if err := tt.remove(ctx, s); err != nil {
				t.Fatal(err)
			}

			apps, err := projects.Get(ctx, "apps")
			if err != nil {
				t.Fatal(err)
			}

			if !slices.Equal(apps.Members, tt.wantMembers) {
				t.Errorf("project members %v; want %v", apps.Members, tt.wantMembers)
			}

			p, err := s.GetPermission(ctx, "libs")
			if err != nil {
				t.Fatal(err)
			}

			for _, r := range p.Rules {
				var users, groups []string

				for _, g := range r.Grants {
					if g.User != "" {
						users = append(users, g.User)
					} else {
						groups = append(groups, g.Group)
					}
				}

				if !slices.Equal(users, tt.wantUsers) || !slices.Equal(groups, tt.wantGroups) {
					t.Errorf(
						"%s grants: users %v, groups %v; want %v, %v",
						r.Resource,
						users,
						groups,
						tt.wantUsers,
						tt.wantGroups,
					)
				}
			}
		})
	}
}

func TestUserChangesKeepAnEnabledAdministrator(t *testing.T) {
	t.Parallel()

	type account struct {
		name     string
		admin    bool
		disabled bool
	}

	demote := func(name string) func(*security.Service) error {
		return func(s *security.Service) error {
			_, err := s.PutUser(t.Context(), security.User{
				Name:  name,
				Email: name + "@example.com",
			})

			return err
		}
	}

	root := account{
		name:  "root",
		admin: true,
	}
	dormant := account{
		name:     "dormant",
		admin:    true,
		disabled: true,
	}

	tests := []struct {
		name     string
		accounts []account
		change   func(*security.Service) error
		refused  bool
	}{
		{
			name: "demoting the only administrator",
			accounts: []account{
				root,
			},
			change:  demote("root"),
			refused: true,
		},
		{
			name: "disabling the only administrator",
			accounts: []account{
				root,
			},
			change: func(s *security.Service) error {
				_, err := s.ReplaceUser(t.Context(), security.User{
					Name:     "root",
					Email:    "root@example.com",
					Admin:    true,
					Disabled: true,
				})

				return err
			},
			refused: true,
		},
		{
			name: "turning the only administrator's sign-ins off",
			accounts: []account{
				root,
			},
			change: func(s *security.Service) error {
				return s.SetDisabled(t.Context(), "root", true)
			},
			refused: true,
		},
		{
			name: "demoting an administrator whose only peer is disabled",
			accounts: []account{
				root,
				dormant,
			},
			change:  demote("root"),
			refused: true,
		},
		{
			name: "deleting an administrator whose only peer is disabled",
			accounts: []account{
				root,
				dormant,
			},
			change: func(s *security.Service) error {
				return s.DeleteUser(t.Context(), "root")
			},
			refused: true,
		},
		{
			name: "demoting one of two administrators",
			accounts: []account{
				root,
				{
					name:  "second",
					admin: true,
				},
			},
			change: demote("root"),
		},
		{
			name: "keeping the only administrator an administrator",
			accounts: []account{
				root,
			},
			change: func(s *security.Service) error {
				_, err := s.PutUser(t.Context(), security.User{
					Name:  "root",
					Email: "new@example.com",
					Admin: true,
				})

				return err
			},
		},
		{
			name: "demoting a disabled administrator",
			accounts: []account{
				root,
				dormant,
			},
			change: demote("dormant"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			db := databasetest.NewDB(t)

			for _, a := range tt.accounts {
				if err := db.CreateUser(ctx, database.CreateUserParams{
					ID:           uuid.NewV7(),
					Username:     a.name,
					PasswordHash: "x",
					Admin:        a.admin,
				}); err != nil {
					t.Fatal(err)
				}

				if err := db.SetUserDisabled(ctx, database.SetUserDisabledParams{
					Username: a.name,
					Disabled: a.disabled,
				}); err != nil {
					t.Fatal(err)
				}
			}

			err := tt.change(security.NewService(db, slog.New(slog.DiscardHandler)))
			if tt.refused && !errors.Is(err, security.ErrInvalid) {
				t.Errorf("change = %v, want %v", err, security.ErrInvalid)
			}

			if !tt.refused && err != nil {
				t.Errorf("change = %v, want it accepted", err)
			}
		})
	}
}

func TestPutUserRefusesNamesThatCannotSignIn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		user    string
		wantErr error
	}{
		{
			name:    "a colon splits Basic credentials",
			user:    "build:bot",
			wantErr: security.ErrInvalid,
		},
		{
			name:    "a space",
			user:    "build bot",
			wantErr: security.ErrInvalid,
		},
		{
			name:    "a slash",
			user:    "build/bot",
			wantErr: security.ErrInvalid,
		},
		{
			name:    "longer than 64 characters",
			user:    strings.Repeat("a", 65),
			wantErr: security.ErrInvalid,
		},
		{
			name:    "differs from an existing user only by case",
			user:    "Existing",
			wantErr: security.ErrExists,
		},
		{
			name: "letters, digits and . @ _ -",
			user: "first.last_2@example-team",
		},
		{
			name: "an existing user replaced under its own name",
			user: "existing",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			s := security.NewService(databasetest.NewDB(t), slog.New(slog.DiscardHandler))

			if _, err := s.PutUser(ctx, security.User{
				Name:     "existing",
				Email:    "existing@example.com",
				Password: "Existing-pass-1",
			}); err != nil {
				t.Fatal(err)
			}

			_, err := s.PutUser(ctx, security.User{
				Name:     tt.user,
				Email:    "user@example.com",
				Password: "User-pass-12345",
			})
			if tt.wantErr == nil && err != nil {
				t.Fatalf("PutUser(%q) = %v, want it accepted", tt.user, err)
			}

			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("PutUser(%q) = %v, want %v", tt.user, err, tt.wantErr)
			}
		})
	}
}
