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
