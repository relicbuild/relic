package project

import (
	"testing"

	"relic/internal/access/auth"
)

func TestRole(t *testing.T) {
	t.Parallel()

	pr := Project{
		Key: "web",
		Members: []Member{
			{
				Name: "alice",
				Role: RoleAdmin,
			},
			{
				Name:  "devs",
				Group: true,
				Role:  RoleViewer,
			},
		},
	}
	alice := auth.Principal{
		Username: "alice",
		Groups: []string{
			"devs",
		},
	}

	tests := []struct {
		name string
		p    auth.Principal
		want Role
	}{
		{
			name: "a direct member",
			p:    alice,
			want: RoleAdmin,
		},
		{
			name: "a token scoped to the member's groups",
			p: auth.TokenScope{
				Kind: auth.ScopeGroups,
				Groups: []string{
					"devs",
				},
			}.Apply(alice),
			want: RoleViewer,
		},
		{
			name: "a token scoped to no groups",
			p: auth.TokenScope{
				Kind: auth.ScopeGroups,
			}.Apply(alice),
		},
		{
			name: "a stranger",
			p: auth.Principal{
				Username: "mallory",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := pr.Role(tt.p); got != tt.want {
				t.Errorf("Role = %q, want %q", got, tt.want)
			}
		})
	}
}
