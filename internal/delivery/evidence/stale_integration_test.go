//go:build integration

package evidence

import (
	"strings"
	"testing"

	"relic/internal/artifacts/repository"
)

func TestEvidenceAboutReplacedContentIsNotVerified(t *testing.T) {
	t.Parallel()

	f := newEvidenceFixture(t)
	if e := f.create(t, f.signedByCI(t)); !e.Verified {
		t.Fatal("evidence signed by a trusted key is not verified")
	}

	if _, err := f.repos.Deploy(t.Context(), repository.DeployInput{
		Repository: "libs-local",
		Path:       "dir/a.txt",
		Body:       strings.NewReader("second"),
		User:       "admin",
	}); err != nil {
		t.Fatal(err)
	}

	out := listed(t, f.evidence)
	if len(out) != 1 || out[0].Verified {
		t.Errorf("evidence after the subject was replaced = %+v; want one unverified", out)
	}
}
