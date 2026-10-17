package security

import (
	"slices"
	"testing"

	"relic/internal/access/auth"
)

func TestPermissionValidateDropsRepeatedSets(t *testing.T) {
	t.Parallel()

	p := Permission{
		Name: "readers",
		Rules: []Rule{
			{
				Resource: ResourceRepository,
				Selector: auth.Selector{
					Sets: []auth.RepositorySet{
						auth.SetAll,
						auth.SetLocal,
						auth.SetAll,
					},
				},
			},
		},
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}

	want := []auth.RepositorySet{
		auth.SetAll,
		auth.SetLocal,
	}
	if got := p.Rules[0].Selector.Sets; !slices.Equal(got, want) {
		t.Errorf("sets = %v, want %v", got, want)
	}
}
