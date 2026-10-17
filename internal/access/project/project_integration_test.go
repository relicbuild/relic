//go:build integration

package project_test

import (
	"errors"
	"testing"

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
