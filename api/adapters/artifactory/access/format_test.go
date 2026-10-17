package access

import (
	"reflect"
	"testing"

	"relic/internal/access/auth"
)

func TestParseScope(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		scope string
		want  auth.TokenScope
		known bool
	}{
		{
			name:  "user",
			scope: "applied-permissions/user",
			want:  auth.UserScope,
			known: true,
		},
		{
			name:  "all groups",
			scope: "member-of-groups:*",
			want:  auth.UserScope,
			known: true,
		},
		{
			name:  "groups",
			scope: "applied-permissions/groups:qa,ops",
			want: auth.TokenScope{
				Kind: auth.ScopeGroups,
				Groups: []string{
					"qa",
					"ops",
				},
			},
			known: true,
		},
		{
			name:  "older groups",
			scope: "member-of-groups:qa",
			want: auth.TokenScope{
				Kind: auth.ScopeGroups,
				Groups: []string{
					"qa",
				},
			},
			known: true,
		},
		{
			name:  "admin",
			scope: "applied-permissions/admin",
			want: auth.TokenScope{
				Kind: auth.ScopeAdmin,
			},
			known: true,
		},
		{
			name:  "service admin",
			scope: "member-of-groups:* jfrt@relic:admin",
			want: auth.TokenScope{
				Kind: auth.ScopeAdmin,
			},
			known: true,
		},
		{
			name:  "a group named admin",
			scope: "applied-permissions/groups:admin",
			want: auth.TokenScope{
				Kind: auth.ScopeGroups,
				Groups: []string{
					"admin",
				},
			},
			known: true,
		},
		{
			name:  "an older scope naming a group admin",
			scope: "member-of-groups:readers,admin",
			want: auth.TokenScope{
				Kind: auth.ScopeGroups,
				Groups: []string{
					"readers",
					"admin",
				},
			},
			known: true,
		},
		{
			name:  "an unknown part ending in :admin",
			scope: "applied-permissions/user other:admin",
			want:  auth.UserScope,
		},
		{
			name:  "groups and admin",
			scope: "applied-permissions/groups:ops applied-permissions/admin",
			want: auth.TokenScope{
				Kind: auth.ScopeAdmin,
				Groups: []string{
					"ops",
				},
			},
			known: true,
		},
		{
			name:  "empty",
			scope: "",
			want:  auth.UserScope,
			known: true,
		},
		{
			name:  "unknown part",
			scope: "applied-permissions/roles:project:dev",
			want:  auth.UserScope,
		},
		{
			name:  "admin and an unknown part",
			scope: "applied-permissions/admin applied-permissions/roles:project:dev",
			want: auth.TokenScope{
				Kind: auth.ScopeAdmin,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, known := ParseScope(tt.scope)
			if !reflect.DeepEqual(got, tt.want) || known != tt.known {
				t.Errorf("ParseScope(%q) = %+v, %v; want %+v, %v", tt.scope, got, known, tt.want, tt.known)
			}
		})
	}
}

func TestScopeRoundTrip(t *testing.T) {
	t.Parallel()

	for _, s := range []auth.TokenScope{
		auth.UserScope,
		{
			Kind: auth.ScopeGroups,
			Groups: []string{
				"a",
				"b",
			},
		},
		{
			Kind: auth.ScopeAdmin,
		},
		{
			Kind: auth.ScopeAdmin,
			Groups: []string{
				"ops",
			},
		},
	} {
		for _, render := range []func(auth.TokenScope) string{
			RenderScope,
			LegacyScope,
		} {
			if got, known := ParseScope(render(s)); !known || !reflect.DeepEqual(got, s) {
				t.Errorf("ParseScope(%q) = %+v, want %+v", render(s), got, s)
			}
		}
	}
}

func TestFormatSubject(t *testing.T) {
	t.Parallel()

	f := Format{}
	if sub := f.Subject("bob"); sub != "jfrt@relic/users/bob" || f.Username(sub) != "bob" {
		t.Errorf("subject %q reads back as %q", sub, f.Username(sub))
	}

	if f.Username("other/users/bob") != "" {
		t.Error("a foreign subject names a user")
	}
}
