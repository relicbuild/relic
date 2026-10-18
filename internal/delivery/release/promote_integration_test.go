//go:build integration

package release

import (
	"errors"
	"testing"

	"relic/internal/artifacts/repository"
)

func TestAPromotionThatPlacesNothingFails(t *testing.T) {
	t.Parallel()

	f := newReleaseFixture(t)

	for _, format := range []repository.Format{
		"npm",
		"pypi",
	} {
		if err := f.repos.CreateRepository(t.Context(), repository.Repository{
			Name:   "uat-" + string(format),
			Class:  repository.ClassLocal,
			Format: format,
			Config: repository.Config{
				Environments: []string{
					"UAT",
				},
			},
		}); err != nil {
			t.Fatal(err)
		}
	}

	f.create(t, "1.0", false, artifactSource("libs/app.bin"))

	_, err := f.releases.Promote(t.Context(), "", "app", "1.0", PromotionSpec{
		Environment: "UAT",
	}, "admin", Access{})
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("Promote where no repository accepts any artifact = %v; want ErrInvalid", err)
	}

	promotions, err := f.releases.Promotions(t.Context(), "", "app", "1.0")
	if err != nil {
		t.Fatal(err)
	}

	if len(promotions) != 0 {
		t.Errorf("promotions recorded = %d; want 0", len(promotions))
	}
}
