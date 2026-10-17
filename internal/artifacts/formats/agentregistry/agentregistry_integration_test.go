//go:build integration

package agentregistry_test

import (
	"log/slog"
	"strings"
	"testing"

	"relic/internal/artifacts/formats/agentregistry"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
)

func TestMain(m *testing.M) { databasetest.Main(m) }

const (
	serverPath = "acme/server.json"
	serverJSON = `{"name": "acme/tools", "version": "1.0.0"}`
)

func TestApprovalCoversOnlyTheApprovedContent(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := databasetest.NewDB(t)
	repos := repository.NewService(db, objectstoretest.NewMemory(), slog.New(slog.DiscardHandler))
	registry := agentregistry.New(db, repos)

	if err := repos.CreateRepository(ctx, repository.Repository{
		Name:   "mcp-local",
		Class:  repository.ClassLocal,
		Format: repository.FormatMCP,
	}); err != nil {
		t.Fatal(err)
	}

	deploy := func(body string) repository.Item {
		t.Helper()

		item, err := repos.Deploy(ctx, repository.DeployInput{
			Repository: "mcp-local",
			Path:       serverPath,
			Body:       strings.NewReader(body),
			User:       "admin",
		})
		if err != nil {
			t.Fatal(err)
		}

		return item
	}
	unapproved := func(item repository.Item) bool {
		t.Helper()

		got, err := registry.Unapproved(ctx, "mcp-local", serverPath, item.Checksums.SHA256)
		if err != nil {
			t.Fatal(err)
		}

		return got
	}

	approvedItem := deploy(serverJSON)

	if err := registry.Approve(ctx, "mcp-local", serverPath, "", "admin"); err != nil {
		t.Fatal(err)
	}

	if unapproved(approvedItem) {
		t.Fatal("the approved content is refused")
	}

	if unindexed := deploy(`{"version": "2.0.0"}`); !unapproved(unindexed) {
		t.Error("content that failed indexing is served under the old content's approval")
	}

	if err := repos.Delete(ctx, "mcp-local", serverPath); err != nil {
		t.Fatal(err)
	}

	if redeployed := deploy(serverJSON); !unapproved(redeployed) {
		t.Error("the approval of a deleted entry approved the same content deployed again")
	}
}

func TestFilesTheRegistryCannotIndexAreNotServed(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := databasetest.NewDB(t)
	repos := repository.NewService(db, objectstoretest.NewMemory(), slog.New(slog.DiscardHandler))
	registry := agentregistry.New(db, repos)

	for name, format := range map[string]repository.Format{
		"mcp-local":     repository.FormatMCP,
		"skills-local":  repository.FormatSkills,
		"generic-local": repository.FormatGeneric,
	} {
		if err := repos.CreateRepository(ctx, repository.Repository{
			Name:   name,
			Class:  repository.ClassLocal,
			Format: format,
		}); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name           string
		repository     string
		path           string
		body           string
		wantUnapproved bool
	}{
		{
			name:           "a server.json with no name",
			repository:     "mcp-local",
			path:           "weather/1.2.0/server.json",
			body:           `{"version": "1.2.0"}`,
			wantUnapproved: true,
		},
		{
			name:           "a server.json that is not JSON",
			repository:     "mcp-local",
			path:           "evil/9/server.json",
			body:           `{"name": "io.example/evil"`,
			wantUnapproved: true,
		},
		{
			name:           "a skill bundle that is not a zip",
			repository:     "skills-local",
			path:           "broken/1.0.0/broken.zip",
			body:           "not a zip",
			wantUnapproved: true,
		},
		{
			name:       "a file that is not a registry entry",
			repository: "mcp-local",
			path:       "weather/README.md",
			body:       "# Weather",
		},
		{
			name:       "a JSON file in another format",
			repository: "generic-local",
			path:       "config/server.json",
			body:       `{"version": "1.2.0"}`,
		},
	}
	for _, tt := range tests {
		item, err := repos.Deploy(ctx, repository.DeployInput{
			Repository: tt.repository,
			Path:       tt.path,
			Body:       strings.NewReader(tt.body),
			User:       "admin",
		})
		if err != nil {
			t.Fatal(err)
		}

		got, err := registry.Unapproved(ctx, tt.repository, tt.path, item.Checksums.SHA256)
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}

		if got != tt.wantUnapproved {
			t.Errorf("%s: unapproved = %v, want %v", tt.name, got, tt.wantUnapproved)
		}
	}
}
