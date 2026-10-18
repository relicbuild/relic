//go:build integration

package release

import (
	"errors"
	"testing"
)

func TestADraftIsNotAReleaseSource(t *testing.T) {
	t.Parallel()

	f := newReleaseFixture(t)
	f.create(t, "1.0", true, artifactSource("libs/app.bin"))

	_, err := f.releases.Create(t.Context(), "", Spec{
		Name:    "app",
		Version: "2.0",
		Sources: []Source{
			{
				Kind: SourceReleases,
				Releases: []Ref{
					{
						Name:    "app",
						Version: "1.0",
					},
				},
			},
		},
	}, "admin", Access{})
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("Create from a draft = %v; want ErrInvalid", err)
	}
}
