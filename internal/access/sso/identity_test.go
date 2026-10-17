package sso

import (
	"slices"
	"testing"

	"github.com/crewjam/saml"
)

func TestClaimedIdentityGroups(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		groups any
		want   []string
	}{
		{
			name: "list",
			groups: []any{
				"Dev Ops",
				"qa",
			},
			want: []string{
				"Dev Ops",
				"qa",
			},
		},
		{
			name:   "one name with a space",
			groups: "Dev Ops",
			want: []string{
				"Dev Ops",
			},
		},
		{
			name:   "comma-separated",
			groups: "Dev Ops, qa,,",
			want: []string{
				"Dev Ops",
				"qa",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			id, err := claimedIdentity(
				Provider{
					GroupsClaim: "groups",
				},
				map[string]any{
					"preferred_username": "alice",
					"groups":             tt.groups,
				},
			)
			if err != nil || !slices.Equal(id.groups, tt.want) {
				t.Errorf("groups = %q, %v; want %q", id.groups, err, tt.want)
			}
		})
	}
}

func TestAssertedIdentitySkipsEmptyValues(t *testing.T) {
	t.Parallel()

	id, err := assertedIdentity(
		Provider{
			UsernameClaim: "uid",
		},
		&saml.Assertion{
			AttributeStatements: []saml.AttributeStatement{
				{
					Attributes: []saml.Attribute{
						{
							Name: "uid",
							Values: []saml.AttributeValue{
								{
									Value: "",
								},
								{
									Value: "alice",
								},
							},
						},
						{
							Name: "email",
							Values: []saml.AttributeValue{
								{
									Value: "",
								},
								{
									Value: "alice@example.com",
								},
							},
						},
					},
				},
			},
		},
	)
	if err != nil || id.username != "alice" || id.email != "alice@example.com" {
		t.Errorf("identity = %+v, %v; want alice with her email", id, err)
	}
}
