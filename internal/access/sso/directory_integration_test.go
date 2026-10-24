//go:build integration

package sso

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"testing"

	"relic/internal/access/security"
	"relic/internal/platform/database/databasetest"
)

func TestDirectoryCreateGroup(t *testing.T) {
	t.Parallel()

	db := databasetest.NewDB(t)
	sec := security.NewService(db, slog.New(slog.DiscardHandler))
	dir := NewDirectory(db, sec)

	for round := range 10 {
		name := fmt.Sprintf("concurrent-%d", round)
		start := make(chan struct{})
		results := make(chan error, 8)

		var creates sync.WaitGroup
		for range cap(results) {
			creates.Go(func() {
				<-start

				_, err := dir.CreateGroup(t.Context(), name, nil)
				results <- err
			})
		}

		close(start)
		creates.Wait()
		close(results)

		created := 0

		for err := range results {
			switch {
			case err == nil:
				created++
			case !errors.Is(err, ErrConflict):
				t.Errorf("create %s: %v", name, err)
			}
		}

		if created != 1 {
			t.Errorf("%d concurrent creates of %s succeeded, want 1", created, name)
		}
	}

	if err := sec.CreateGroup(t.Context(), security.Group{
		Name:  "administrators",
		Admin: true,
	}, nil); err != nil {
		t.Fatal(err)
	}

	if _, err := dir.CreateGroup(t.Context(), "administrators", nil); !errors.Is(err, ErrConflict) {
		t.Errorf("create an existing group: %v, want ErrConflict", err)
	}

	if g, err := sec.GetGroup(t.Context(), "administrators", false); err != nil || !g.Admin {
		t.Errorf("existing group after a refused create = %+v %v", g, err)
	}
}

func TestDirectoryLeavesPrivilegedAndRelicManagedGroupsAlone(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := databasetest.NewDB(t)
	sec := security.NewService(db, slog.New(slog.DiscardHandler))
	dir := NewDirectory(db, sec)

	if err := sec.CreateUser(ctx, security.User{
		Name:     "bob",
		Email:    "bob@example.com",
		Password: "Bob-password-1",
	}); err != nil {
		t.Fatal(err)
	}

	for _, g := range []security.Group{
		{
			Name:  "administrators",
			Admin: true,
		},
		{
			Name:   "auditors",
			Viewer: true,
		},
		{
			Name: "devs",
		},
	} {
		if err := sec.CreateGroup(ctx, g, []string{
			"bob",
		}); err != nil {
			t.Fatal(err)
		}
	}

	alice, err := dir.CreateUser(ctx, DirectoryUser{
		UserName: "alice",
		Active:   true,
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, group := range []string{
		"administrators",
		"auditors",
	} {
		if err := dir.ChangeMembers(ctx, group, []string{
			alice.ID,
		}, nil, false); !errors.Is(err, ErrImmutable) {
			t.Errorf("SCIM adding to %s = %v, want ErrImmutable", group, err)
		}

		if err := dir.DeleteGroup(ctx, group); !errors.Is(err, ErrImmutable) {
			t.Errorf("SCIM deleting %s = %v, want ErrImmutable", group, err)
		}

		g, err := sec.GetGroup(ctx, group, true)
		if err != nil || len(g.Members) != 1 || g.Members[0] != "bob" {
			t.Errorf("%s after SCIM's attempts = %+v %v", group, g, err)
		}
	}

	if err := dir.ChangeMembers(ctx, "devs", []string{
		alice.ID,
	}, nil, false); err != nil {
		t.Errorf("SCIM adding to devs: %v", err)
	}

	if err := dir.DeleteGroup(ctx, "devs"); !errors.Is(err, ErrImmutable) {
		t.Errorf("SCIM deleting devs, which has a member Relic manages = %v, want ErrImmutable", err)
	}

	if _, err := dir.CreateGroup(ctx, "idp-team", []string{
		alice.ID,
	}); err != nil {
		t.Fatal(err)
	}

	if err := dir.DeleteGroup(ctx, "idp-team"); err != nil {
		t.Errorf("SCIM deleting a group only it manages: %v", err)
	}
}

func TestDirectoryKeepsTheLastEnabledAdministrator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		change func(*Directory, DirectoryUser) error
	}{
		{
			name: "replacing the user as inactive",
			change: func(dir *Directory, u DirectoryUser) error {
				u.Active = false
				_, err := dir.UpdateUser(t.Context(), u)

				return err
			},
		},
		{
			name: "patching the user inactive",
			change: func(dir *Directory, u DirectoryUser) error {
				after := u
				after.Active = false
				_, err := dir.PatchUser(t.Context(), u, after)

				return err
			},
		},
		{
			name: "deleting the user",
			change: func(dir *Directory, u DirectoryUser) error {
				return dir.DeleteUser(t.Context(), u.ID)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			db := databasetest.NewDB(t)
			sec := security.NewService(db, slog.New(slog.DiscardHandler))
			dir := NewDirectory(db, sec)

			u, err := dir.CreateUser(ctx, DirectoryUser{
				UserName: "alice",
				Email:    "alice@example.com",
				Active:   true,
			})
			if err != nil {
				t.Fatal(err)
			}

			if _, err := sec.PutUser(ctx, security.User{
				Name:  "alice",
				Email: "alice@example.com",
				Admin: true,
			}); err != nil {
				t.Fatal(err)
			}

			if err := tt.change(dir, u); !errors.Is(err, ErrInvalidValue) {
				t.Errorf("change = %v, want %v", err, ErrInvalidValue)
			}

			if after, err := dir.User(ctx, u.ID); err != nil || !after.Active {
				t.Errorf("administrator after a refused change = %+v %v, want it active", after, err)
			}
		})
	}
}

func TestSavingAGroupKeepsProvisionedMembers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// saved are the members an administrator saves after provisioning.
		saved []string
		// deprovision removes Sam through the directory after the save.
		deprovision bool
		want        []string
	}{
		{
			name: "an exported group saved back, then Sam deprovisioned",
			saved: []string{
				"Sam.Smith",
				"alice",
			},
			deprovision: true,
			want: []string{
				"alice",
			},
		},
		{
			name: "an administrator's list leaves Sam alone",
			saved: []string{
				"alice",
			},
			want: []string{
				"Sam.Smith",
				"alice",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			db := databasetest.NewDB(t)
			sec := security.NewService(db, slog.New(slog.DiscardHandler))
			dir := NewDirectory(db, sec)

			sam, err := dir.CreateUser(ctx, DirectoryUser{
				UserName: "Sam.Smith",
				Active:   true,
			})
			if err != nil {
				t.Fatal(err)
			}

			if err := sec.CreateUser(ctx, security.User{
				Name:     "alice",
				Email:    "alice@example.com",
				Password: "Alice-password-1",
			}); err != nil {
				t.Fatal(err)
			}

			if _, err := dir.CreateGroup(ctx, "eng", []string{
				sam.ID,
			}); err != nil {
				t.Fatal(err)
			}

			if _, err := sec.PutGroup(ctx, security.Group{
				Name: "eng",
			}, tt.saved); err != nil {
				t.Fatal(err)
			}

			if tt.deprovision {
				if err := dir.ChangeMembers(ctx, "eng", nil, []string{
					sam.ID,
				}, false); err != nil {
					t.Fatal(err)
				}
			}

			g, err := sec.GetGroup(ctx, "eng", true)
			if err != nil {
				t.Fatal(err)
			}

			if !slices.Equal(g.Members, tt.want) {
				t.Errorf("members = %v, want %v", g.Members, tt.want)
			}
		})
	}
}
