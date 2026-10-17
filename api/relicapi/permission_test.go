package relicapi

import (
	"slices"
	"testing"

	"relic/internal/access/auth"
	"relic/internal/access/security"
)

func TestPermissionMessageLeavesOutAnEmptySelector(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		rule         security.Rule
		wantSelector bool
		repositories []string
	}{
		{
			name: "build rule without repositories",
			rule: security.Rule{
				Resource: security.ResourceBuild,
				Include: []string{
					"**",
				},
			},
		},
		{
			name: "release rule without repositories",
			rule: security.Rule{
				Resource: security.ResourceRelease,
			},
		},
		{
			name: "repository rule naming repositories",
			rule: security.Rule{
				Resource: security.ResourceRepository,
				Selector: auth.Selector{
					Repositories: []string{
						"libs",
					},
				},
			},
			wantSelector: true,
			repositories: []string{
				"libs",
			},
		},
		{
			name: "repository rule naming a set",
			rule: security.Rule{
				Resource: security.ResourceRepository,
				Selector: auth.Selector{
					Sets: []auth.RepositorySet{
						auth.SetAll,
					},
				},
			},
			wantSelector: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			m := PermissionMessage(security.Permission{
				Name: "p",
				Rules: []security.Rule{
					tt.rule,
				},
			})

			selector := m.GetRules()[0].GetSelector()
			if (selector != nil) != tt.wantSelector {
				t.Fatalf("selector = %v, want present: %v", selector, tt.wantSelector)
			}

			if !slices.Equal(selector.GetRepositories(), tt.repositories) {
				t.Errorf("repositories = %v, want %v", selector.GetRepositories(), tt.repositories)
			}
		})
	}
}
