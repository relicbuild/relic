//go:build integration

package release

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"relic/internal/access/project"
	"relic/internal/artifacts/repository"
)

func TestReleasesNeedAnExistingProject(t *testing.T) {
	t.Parallel()

	f := newReleaseFixture(t)
	f.releases.SetProjects(project.New(f.db))

	spec := Spec{
		Name:    "app",
		Version: "1.0",
		Sources: artifactSource("libs/app.bin"),
	}

	if _, err := f.releases.Create(t.Context(), "nosuch", spec, "admin", Access{}); !errors.Is(err, ErrInvalid) {
		t.Errorf("Create in a missing project = %v; want ErrInvalid", err)
	}

	missing := f.releases.layout.Repository("nosuch")
	if _, err := f.repos.GetRepository(t.Context(), missing); !errors.Is(err, repository.ErrRepositoryNotFound) {
		t.Errorf("GetRepository(%s) after the refused create = %v; want ErrRepositoryNotFound", missing, err)
	}

	if _, err := project.New(f.db).Put(t.Context(), project.Project{
		Key: "acme",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := f.releases.Create(t.Context(), "acme", spec, "admin", Access{}); err != nil {
		t.Fatalf("Create in an existing project = %v", err)
	}

	exported, err := f.releases.Export(t.Context(), "acme", "app", "1.0", "admin")
	if err != nil {
		t.Fatal(err)
	}

	body, err := f.repos.Open(t.Context(), exported, nil)
	if err != nil {
		t.Fatal(err)
	}

	archive, err := io.ReadAll(body)
	_ = body.Close()

	if err != nil {
		t.Fatal(err)
	}

	other := newReleaseFixture(t)
	other.releases.SetProjects(project.New(other.db))
	other.trustReleasesOf(t, f)

	if _, err := other.releases.Import(
		t.Context(),
		bytes.NewReader(archive),
		int64(len(archive)),
		"admin",
	); !errors.Is(
		err,
		ErrInvalid,
	) || errors.Is(err, ErrUntrustedKey) {
		t.Errorf("Import into a server without the archive's project = %v; want ErrInvalid", err)
	}
}
