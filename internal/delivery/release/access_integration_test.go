//go:build integration

package release

import (
	"errors"
	"log/slog"
	"strings"
	"testing"

	"relic/internal/artifacts/itemquery"
	"relic/internal/delivery/buildinfo"
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

func TestABuildSourceNeedsReadOnTheBuild(t *testing.T) {
	t.Parallel()

	f := newReleaseFixture(t)
	f.releases.builds = buildinfo.NewService(f.db, f.repos, nil, slog.New(slog.DiscardHandler))

	var asked []string

	_, err := f.releases.Create(t.Context(), "", Spec{
		Name:    "app",
		Version: "1.0",
		Sources: []Source{
			{
				Kind: SourceBuilds,
				Builds: []BuildRef{
					{
						Name:   "app-build",
						Number: "7",
					},
				},
			},
		},
	}, "developer", Access{
		CanReadBuild: func(repository, name string) bool {
			asked = append(asked, repository+"/"+name)

			return false
		},
	})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "build app-build/7 not found") {
		t.Errorf("Create from a build the caller cannot read = %v; want the answer for a missing build", err)
	}

	want := f.releases.builds.Repository("") + "/app-build"
	if len(asked) != 1 || asked[0] != want {
		t.Errorf("build read checks = %q; want [%q]", asked, want)
	}
}
