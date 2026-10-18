//go:build integration

package integration

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
)

func skillBundle(t *testing.T, files map[string]string) string {
	t.Helper()

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}

		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.String()
}

func TestAgentRegistry(t *testing.T) {
	t.Parallel()
	srv := server(t)
	ctx := t.Context()

	repos := relicv1connect.NewRepositoryServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))
	for name, format := range map[string]relicv1.Format{
		"agent-mcp":    relicv1.Format_FORMAT_MCP,
		"agent-skills": relicv1.Format_FORMAT_SKILLS,
	} {
		if _, err := repos.CreateRepository(ctx, &relicv1.CreateRepositoryRequest{
			Repository: &relicv1.Repository{
				Name:   name,
				Class:  relicv1.RepositoryClass_REPOSITORY_CLASS_LOCAL,
				Format: format,
			},
		}); err != nil {
			t.Fatal(err)
		}
	}

	upload := func(repo, path, body string) {
		t.Helper()

		if resp, got := do(t, srv, call{
			method: http.MethodPut,
			path:   "/api/v1/files/" + repo + "/" + path,
			body:   body,
		}); resp.StatusCode != http.StatusCreated {
			t.Fatalf("upload %s/%s = %d %s", repo, path, resp.StatusCode, got)
		}
	}

	server := func(version, description string) string {
		return `{"$schema":"https://static.modelcontextprotocol.io/schemas/2025-09-29/server.schema.json",` +
			`"name":"io.github.acme/weather","version":"` + version + `","description":"` + description + `",` +
			`"remotes":[{"type":"streamable-http","url":"https://weather.example.com/mcp"}]}`
	}

	upload("agent-mcp", "weather/1.0.0/server.json", server("1.0.0", "Forecasts"))
	upload("agent-mcp", "weather/1.1.0/server.json", server("1.1.0", "Forecasts and alerts"))
	upload("agent-skills", "pdf-forms.zip", skillBundle(t, map[string]string{
		"pdf-forms/SKILL.md":  "---\nname: pdf-forms\ndescription: Fill PDF forms\nmetadata:\n  version: 2.0.0\n---\n# PDF forms\n",
		"pdf-forms/fill.py":   "print('fill')\n",
		"pdf-forms/README.md": "docs\n",
	}))
	upload("agent-skills", "broken.zip", skillBundle(t, map[string]string{
		"notes.txt": "no skill here",
	}))

	registry := relicv1connect.NewAgentRegistryServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))

	list := func(c relicv1connect.AgentRegistryServiceClient, repo string) []*relicv1.RegistryEntry {
		t.Helper()

		r, err := c.ListRegistryEntries(ctx, &relicv1.ListRegistryEntriesRequest{
			Repository: repo,
		})
		if err != nil {
			t.Fatal(err)
		}

		return r.GetEntries()
	}

	entries := list(registry, "agent-mcp")
	if len(entries) != 2 || entries[0].GetName() != "io.github.acme/weather" || entries[0].GetApproved() {
		t.Fatalf("mcp entries = %v", entries)
	}

	skills := list(registry, "agent-skills")
	if len(skills) != 1 || skills[0].GetName() != "pdf-forms" || skills[0].GetVersion() != "2.0.0" ||
		skills[0].GetDescription() != "Fill PDF forms" {
		t.Fatalf("skill entries = %v", skills)
	}

	servers := func(path string) (int, map[string]any) {
		t.Helper()

		resp, body := do(t, srv, call{
			method: http.MethodGet,
			path:   "/api/mcp/agent-mcp/v0/servers" + path,
		})

		var doc map[string]any

		_ = json.Unmarshal([]byte(body), &doc)

		return resp.StatusCode, doc
	}

	if code, doc := servers(""); code != http.StatusOK || doc["metadata"].(map[string]any)["count"].(float64) != 0 {
		t.Errorf("unapproved servers listed: %d %v", code, doc)
	}

	approve := func(c relicv1connect.AgentRegistryServiceClient, repo, path string) error {
		_, err := c.ApproveRegistryEntry(ctx, &relicv1.ApproveRegistryEntryRequest{
			Repository: repo,
			Path:       path,
		})

		return err
	}

	reviewed := ""

	for _, e := range entries {
		if e.GetPath() == "weather/1.0.0/server.json" {
			reviewed = e.GetSha256()
		}
	}

	if _, err := registry.ApproveRegistryEntry(ctx, &relicv1.ApproveRegistryEntryRequest{
		Repository: "agent-mcp",
		Path:       "weather/1.0.0/server.json",
		Sha256:     strings.Repeat("0", 64),
	}); code(err) != connect.CodeInvalidArgument {
		t.Errorf("approving content other than the reviewed one: %v", err)
	}

	if _, err := registry.ApproveRegistryEntry(ctx, &relicv1.ApproveRegistryEntryRequest{
		Repository: "agent-mcp",
		Path:       "weather/1.0.0/server.json",
		Sha256:     reviewed,
	}); reviewed == "" || err != nil {
		t.Errorf("approving the reviewed content %q: %v", reviewed, err)
	}

	if _, err := registry.RevokeRegistryApproval(ctx, &relicv1.RevokeRegistryApprovalRequest{
		Repository: "agent-mcp",
		Path:       "weather/1.0.0/server.json",
	}); err != nil {
		t.Fatal(err)
	}

	sec := relicv1connect.NewSecurityServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))
	if _, err := sec.PutUser(ctx, &relicv1.PutUserRequest{
		User: &relicv1.User{
			Name:     "skilled",
			Email:    "skilled@example.com",
			Password: "Skilled-pass-12345",
		},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := sec.PutPermission(ctx, &relicv1.PutPermissionRequest{
		Permission: &relicv1.Permission{
			Name: "agent-registries",
			Rules: []*relicv1.PermissionRule{
				{
					Resource: relicv1.ResourceKind_RESOURCE_KIND_REPOSITORY,
					Selector: &relicv1.RepositorySelector{
						Repositories: []string{
							"agent-mcp",
							"agent-skills",
						},
					},
					Grants: []*relicv1.PermissionGrant{
						{
							Principal: &relicv1.PermissionGrant_User{
								User: "skilled",
							},
							Actions: []relicv1.Action{
								relicv1.Action_ACTION_READ,
							},
						},
					},
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	user := relicv1connect.NewAgentRegistryServiceClient(apiClient(relicHTTP("skilled", "Skilled-pass-12345"), srv.URL))
	if err := approve(user, "agent-skills", "pdf-forms.zip"); code(err) != connect.CodePermissionDenied {
		t.Errorf("a user approving: %v", err)
	}

	if got := list(user, "agent-skills"); len(got) != 0 {
		t.Errorf("a user sees unapproved skills: %v", got)
	}

	download := func(path string) (int, string) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}

		resp, err := relicHTTP("skilled", "Skilled-pass-12345").Do(req)
		if err != nil {
			t.Fatal(err)
		}

		defer func() {
			_ = resp.Body.Close()
		}()

		body, _ := io.ReadAll(resp.Body)

		return resp.StatusCode, string(body)
	}

	if resp, _ := do(t, srv, call{
		method: http.MethodGet,
		path:   "/api/v1/files/agent-skills/pdf-forms.zip",
	}); resp.StatusCode != http.StatusOK {
		t.Errorf("an administrator reviewing an unapproved entry = %d", resp.StatusCode)
	}

	for _, p := range []string{
		"/api/v1/files/agent-skills/pdf-forms.zip",
		"/artifactory/agent-skills/pdf-forms.zip",
		"/api/v1/files/agent-mcp/weather/1.0.0/server.json",
	} {
		if code, body := download(p); code != http.StatusForbidden || !strings.Contains(body, "not approved") {
			t.Errorf("download of an unapproved entry %s = %d %s", p, code, body)
		}
	}

	for _, e := range []struct {
		repo, path string
	}{
		{
			"agent-skills",
			"pdf-forms.zip",
		},
		{
			"agent-mcp",
			"weather/1.0.0/server.json",
		},
		{
			"agent-mcp",
			"weather/1.1.0/server.json",
		},
	} {
		if err := approve(registry, e.repo, e.path); err != nil {
			t.Fatal(err)
		}
	}

	if got := list(user, "agent-skills"); len(got) != 1 || !got[0].GetApproved() {
		t.Errorf("a user's approved skills = %v", got)
	}

	for _, p := range []string{
		"/api/v1/files/agent-skills/pdf-forms.zip",
		"/artifactory/agent-skills/pdf-forms.zip",
	} {
		if code, body := download(p); code != http.StatusOK {
			t.Errorf("download of an approved entry %s = %d %s", p, code, body)
		}
	}

	if code, doc := servers(""); code != http.StatusOK || doc["metadata"].(map[string]any)["count"].(float64) != 2 {
		t.Errorf("approved servers = %d %v", code, doc)
	}

	code, latest := servers("/io.github.acme/weather/versions/latest")
	if code != http.StatusOK || latest["server"].(map[string]any)["version"] != "1.1.0" {
		t.Errorf("latest = %d %v", code, latest)
	}

	code, older := servers("/io.github.acme/weather/versions/1.0.0")
	olderMeta, _ := older["_meta"].(map[string]any)

	official, _ := olderMeta["io.modelcontextprotocol.registry/official"].(map[string]any)
	if code != http.StatusOK || official["isLatest"] != false {
		t.Errorf("version 1.0.0 = %d %v; want it not marked latest", code, older)
	}

	upload("agent-mcp", "weather/1.1.0/server.json", server("1.1.0", "Changed after approval"))

	if code, _ := download("/api/v1/files/agent-mcp/weather/1.1.0/server.json"); code != http.StatusForbidden {
		t.Errorf("download of an entry changed after its approval = %d, want refused", code)
	}

	code, latest = servers("/io.github.acme/weather/versions/latest")
	if code != http.StatusOK || latest["server"].(map[string]any)["version"] != "1.0.0" {
		t.Errorf("after the approved 1.1.0 changed, latest = %d %v; want 1.0.0", code, latest)
	}

	if _, err := registry.RevokeRegistryApproval(ctx, &relicv1.RevokeRegistryApprovalRequest{
		Repository: "agent-mcp",
		Path:       "weather/1.0.0/server.json",
	}); err != nil {
		t.Fatal(err)
	}

	if code, _ := servers("/io.github.acme/weather/versions/latest"); code != http.StatusNotFound {
		t.Errorf("after the revocation latest = %d, want not found", code)
	}

	artifacts := relicv1connect.NewArtifactServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))
	if _, err := artifacts.DeleteItem(ctx, &relicv1.DeleteItemRequest{
		Repository: "agent-skills",
		Path:       "pdf-forms.zip",
	}); err != nil {
		t.Fatal(err)
	}

	if got := list(registry, "agent-skills"); len(got) != 0 {
		t.Errorf("a deleted bundle is still listed: %v", got)
	}
}
