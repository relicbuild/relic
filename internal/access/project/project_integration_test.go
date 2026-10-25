//go:build integration

package project_test

import (
	"context"
	"errors"
	"testing"

	"relic/internal/access/auth"
	"relic/internal/access/project"
	"relic/internal/platform/database/databasetest"
)

func TestMain(m *testing.M) { databasetest.Main(m) }

func TestRemoveMember(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	svc := project.New(databasetest.NewDB(t))

	if _, err := svc.Put(ctx, project.Project{
		Key:  "web",
		Name: "Web storefront",
		Members: []project.Member{
			{
				Name: "bob",
				Role: project.RoleDeveloper,
			},
			{
				Name:  "builders",
				Group: true,
				Role:  project.RoleDeveloper,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		member      string
		group       bool
		wantErr     error
		wantMembers int
	}{
		{
			name:        "a group named as a user",
			member:      "builders",
			wantErr:     project.ErrMemberNotFound,
			wantMembers: 2,
		},
		{
			name:        "someone who is not a member",
			member:      "ghost",
			wantErr:     project.ErrMemberNotFound,
			wantMembers: 2,
		},
		{
			name:        "a group",
			member:      "builders",
			group:       true,
			wantMembers: 1,
		},
		{
			name:        "a user",
			member:      "bob",
			wantMembers: 0,
		},
	}
	for _, tt := range tests {
		_, err := svc.RemoveMember(ctx, "web", tt.member, tt.group)
		if !errors.Is(err, tt.wantErr) {
			t.Errorf("%s: RemoveMember = %v, want %v", tt.name, err, tt.wantErr)
		}

		p, err := svc.Get(ctx, "web")
		if err != nil {
			t.Fatal(err)
		}

		if len(p.Members) != tt.wantMembers {
			t.Errorf("%s: members = %+v, want %d", tt.name, p.Members, tt.wantMembers)
		}
	}
}

func TestPermissionsCoverTheProjectsBuildsAndReleases(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	svc := project.New(databasetest.NewDB(t))
	svc.SetRepositoryProjects(func(context.Context) (map[string]string, error) {
		return map[string]string{}, nil
	})
	svc.SetStorageRepositories(func(key string) []string {
		return []string{
			key + "-build-info",
			key + "-release-bundles-v2",
		}
	})

	if _, err := svc.Put(ctx, project.Project{
		Key:  "acme",
		Name: "Acme",
		Members: []project.Member{
			{
				Name: "paula",
				Role: project.RoleAdmin,
			},
			{
				Name: "dave",
				Role: project.RoleDeveloper,
			},
			{
				Name: "olga",
				Role: project.RoleViewer,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	all, err := svc.Permissions(ctx)
	if err != nil {
		t.Fatal(err)
	}

	perms := auth.Permissions{
		All: all,
	}

	tests := []struct {
		name   string
		user   string
		action auth.Action
		want   bool
	}{
		{
			name:   "an admin deletes",
			user:   "paula",
			action: auth.ActionDelete,
			want:   true,
		},
		{
			name:   "a developer publishes",
			user:   "dave",
			action: auth.ActionWrite,
			want:   true,
		},
		{
			name:   "a viewer reads",
			user:   "olga",
			action: auth.ActionRead,
			want:   true,
		},
		{
			name:   "a viewer publishes",
			user:   "olga",
			action: auth.ActionWrite,
		},
		{
			name:   "someone outside the project reads",
			user:   "mallory",
			action: auth.ActionRead,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := auth.Subject{
				User: tt.user,
			}

			if got := perms.CanBuild(s, "acme-build-info", "acmeb", tt.action); got != tt.want {
				t.Errorf("CanBuild = %v, want %v", got, tt.want)
			}

			if got := perms.CanRelease(s, "acme-release-bundles-v2", "rel", "1", tt.action); got != tt.want {
				t.Errorf("CanRelease = %v, want %v", got, tt.want)
			}

			if perms.CanBuild(s, "build-info", "acmeb", tt.action) {
				t.Error("CanBuild outside the project's repository = true, want false")
			}
		})
	}
}
