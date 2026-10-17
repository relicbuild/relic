//go:build integration

package release

import (
	"errors"
	"strings"
	"testing"

	"relic/internal/artifacts/itemquery"
)

func querySource(criteria itemquery.Node) []Source {
	return []Source{
		{
			Kind: SourceQuery,
			Query: itemquery.Query{
				Criteria: criteria,
			},
		},
	}
}

func TestAQuerySourceLeavesOutFilesTheCallerCannotRead(t *testing.T) {
	t.Parallel()

	f := newReleaseFixture(t)
	f.deploy(t, "prod", "secret.bin", "secret")

	developer := Access{
		CanRead: func(repository, _ string) bool {
			return repository != "prod"
		},
	}

	b, err := f.releases.Create(t.Context(), "", Spec{
		Name:    "app",
		Version: "1.0",
		Sources: querySource(itemquery.Compare{
			Field: itemquery.FieldName,
			Op:    itemquery.OpMatch,
			Value: "*.bin",
		}),
	}, "developer", developer)
	if err != nil {
		t.Fatal(err)
	}

	for _, a := range b.Artifacts {
		if a.Repository == "prod" {
			t.Errorf("release artifacts include %s/%s, which the caller cannot read", a.Repository, a.Path)
		}
	}

	_, err = f.releases.Create(t.Context(), "", Spec{
		Name:    "app",
		Version: "2.0",
		Sources: querySource(itemquery.Compare{
			Field: itemquery.FieldRepository,
			Op:    itemquery.OpEqual,
			Value: "prod",
		}),
	}, "developer", developer)
	if !errors.Is(err, ErrInvalid) || strings.Contains(err.Error(), "secret.bin") {
		t.Errorf("Create from a query of only unreadable files = %v; want ErrInvalid without their paths", err)
	}
}
