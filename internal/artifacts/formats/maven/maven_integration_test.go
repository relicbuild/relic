//go:build integration

package maven_test

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"

	"relic/internal/artifacts/formats/maven"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
)

func TestMain(m *testing.M) { databasetest.Main(m) }

func newMaven(t *testing.T) (*repository.Service, *maven.Service) {
	t.Helper()

	repos := repository.NewService(databasetest.NewDB(t), objectstoretest.NewMemory(), slog.New(slog.DiscardHandler))
	if err := repos.CreateRepository(
		t.Context(),
		repository.Repository{
			Name:   "libs",
			Class:  repository.ClassLocal,
			Format: maven.Format,
		},
	); err != nil {
		t.Fatal(err)
	}

	return repos, maven.NewService(repos, slog.New(slog.DiscardHandler))
}

func deployJar(t *testing.T, repos *repository.Service, version string) {
	t.Helper()

	if _, err := repos.Deploy(
		context.WithoutCancel(t.Context()),
		repository.DeployInput{
			Repository: "libs",
			Path:       "org/example/lib/" + version + "/lib-" + version + ".jar",
			Body:       strings.NewReader("jar " + version),
			User:       "admin",
		},
	); err != nil {
		t.Errorf("deploy %s: %v", version, err)
	}
}

func versions(t *testing.T, repos *repository.Service, dir string) []string {
	t.Helper()

	item, err := repos.Stat(t.Context(), "libs", dir+"/"+maven.MetadataFile)
	if err != nil {
		t.Fatalf("stat %s metadata: %v", dir, err)
	}

	body, err := repos.Open(t.Context(), item, nil)
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		_ = body.Close()
	}()

	md, err := maven.Parse(body)
	if err != nil {
		t.Fatal(err)
	}

	if md.Versioning == nil {
		return nil
	}

	return md.Versioning.Versions
}

func TestConcurrentDeploysAllAppearInMetadata(t *testing.T) {
	t.Parallel()

	repos, _ := newMaven(t)

	const n = 20

	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			deployJar(t, repos, fmt.Sprintf("1.%d", i))
		})
	}

	wg.Wait()

	if got := versions(t, repos, "org/example/lib"); len(got) != n {
		t.Errorf("metadata lists %d versions %v, want %d", len(got), got, n)
	}
}

func TestDeployedGroupMetadataKeepsItsPlugins(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	repos, _ := newMaven(t)

	if _, err := repos.Deploy(ctx, repository.DeployInput{
		Repository: "libs",
		Path:       "org/example/demo-maven-plugin/1.0/demo-maven-plugin-1.0.jar",
		Body:       strings.NewReader("plugin jar"),
		User:       "admin",
	}); err != nil {
		t.Fatal(err)
	}

	group := `<?xml version="1.0" encoding="UTF-8"?>
<metadata><plugins><plugin><name>Demo</name><prefix>demo</prefix>` +
		`<artifactId>demo-maven-plugin</artifactId></plugin></plugins></metadata>`
	if _, err := repos.Deploy(ctx, repository.DeployInput{
		Repository: "libs",
		Path:       "org/example/" + maven.MetadataFile,
		Body:       strings.NewReader(group),
		User:       "admin",
	}); err != nil {
		t.Fatal(err)
	}

	item, err := repos.Stat(ctx, "libs", "org/example/"+maven.MetadataFile)
	if err != nil {
		t.Fatal(err)
	}

	body, err := repos.Open(ctx, item, nil)
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		_ = body.Close()
	}()

	md, err := maven.Parse(body)
	if err != nil {
		t.Fatal(err)
	}

	if len(md.Plugins) != 1 || md.Plugins[0].Prefix != "demo" {
		t.Errorf("group metadata plugins = %+v, want the deployed demo plugin", md.Plugins)
	}
}

func TestDeletingAVersionUpdatesTheMetadata(t *testing.T) {
	t.Parallel()

	repos, _ := newMaven(t)
	deployJar(t, repos, "1.0")
	deployJar(t, repos, "2.0")

	if err := repos.Delete(t.Context(), "libs", "org/example/lib/2.0"); err != nil {
		t.Fatal(err)
	}

	if got := versions(t, repos, "org/example/lib"); !slices.Equal(got, []string{
		"1.0",
	}) {
		t.Errorf("versions after deleting 2.0 = %v, want [1.0]", got)
	}
}
