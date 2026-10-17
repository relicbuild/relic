//go:build integration

package security_test

import (
	"context"
	"log/slog"
	"slices"
	"testing"
	"time"
	"uuid"

	"relic/internal/access/auth"
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
		name       string
		remove     func(context.Context, *security.Service) error
		wantUsers  []string
		wantGroups []string
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
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			db := databasetest.NewDB(t)
			s := security.NewService(db, slog.New(slog.DiscardHandler))

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

			if err := tt.remove(ctx, s); err != nil {
				t.Fatal(err)
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
