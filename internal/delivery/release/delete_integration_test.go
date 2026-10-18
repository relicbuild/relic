//go:build integration

package release

import (
	"errors"
	"testing"
)

func TestDeleteRemovesAVersionWhoseRepositoryIsGone(t *testing.T) {
	t.Parallel()

	f := newReleaseFixture(t)
	b := f.create(t, "1.0", false, artifactSource("libs/app.bin"))

	if err := f.repos.DeleteRepository(t.Context(), b.Repository); err != nil {
		t.Fatal(err)
	}

	if err := f.releases.Delete(t.Context(), "", "app", "1.0"); err != nil {
		t.Fatalf("Delete after its repository was deleted = %v; want nil", err)
	}

	if _, err := f.releases.Get(t.Context(), "", "app", "1.0"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after Delete = %v; want ErrNotFound", err)
	}
}
