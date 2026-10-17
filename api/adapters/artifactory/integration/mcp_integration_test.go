//go:build integration

package integration

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
)

type basicAuth struct {
	user, password string
}

func (b basicAuth) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.SetBasicAuth(b.user, b.password)

	return http.DefaultTransport.RoundTrip(r)
}

func mcpSession(t *testing.T, srv *httptest.Server, user, password string) (*mcp.ClientSession, error) {
	t.Helper()

	client := mcp.NewClient(&mcp.Implementation{
		Name:    "test",
		Version: "1",
	}, nil)

	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: srv.URL + "/mcp",
		HTTPClient: &http.Client{
			Transport: basicAuth{
				user:     user,
				password: password,
			},
		},
		MaxRetries:           -1,
		DisableStandaloneSSE: true,
	}, nil)
	if err == nil {
		t.Cleanup(func() {
			_ = session.Close()
		})
	}

	return session, err
}

func toolText(t *testing.T, s *mcp.ClientSession, tool string, args map[string]any) (string, bool) {
	t.Helper()

	res, err := s.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      tool,
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}

	var out strings.Builder

	for _, c := range res.Content {
		if text, ok := c.(*mcp.TextContent); ok {
			out.WriteString(text.Text)
		}
	}

	return out.String(), res.IsError
}

func TestMCPServer(t *testing.T) {
	t.Parallel()
	srv := server(t)
	ctx := t.Context()

	for _, repo := range []string{
		"mcp-open",
		"mcp-secret",
	} {
		do(t, srv, call{
			method: http.MethodPut,
			path:   "/artifactory/api/repositories/" + repo,
			body:   `{"rclass":"local","packageType":"generic"}`,
		})
		do(t, srv, call{
			method: http.MethodPut,
			path:   "/artifactory/" + repo + "/tools/mcp-widget-1.0.tgz",
			body:   "widget " + repo,
		})
	}

	sec := relicv1connect.NewSecurityServiceClient(relicHTTP("admin", "password"), srv.URL+"/api")
	if _, err := sec.PutUser(ctx, connect.NewRequest(&relicv1.PutUserRequest{
		User: &relicv1.User{
			Name:     "agent",
			Email:    "agent@example.com",
			Password: "Agent-pass-12345",
		},
	})); err != nil {
		t.Fatal(err)
	}

	if _, err := sec.PutPermission(ctx, connect.NewRequest(&relicv1.PutPermissionRequest{
		Permission: &relicv1.Permission{
			Name: "mcp-open-read",
			Rules: []*relicv1.PermissionRule{
				{
					Resource: relicv1.ResourceKind_RESOURCE_KIND_REPOSITORY,
					Selector: &relicv1.RepositorySelector{
						Repositories: []string{
							"mcp-open",
						},
					},
					Grants: []*relicv1.PermissionGrant{
						{
							Principal: &relicv1.PermissionGrant_User{
								User: "agent",
							},
							Actions: []relicv1.Action{
								relicv1.Action_ACTION_READ,
							},
						},
					},
				},
			},
		},
	})); err != nil {
		t.Fatal(err)
	}

	if _, err := mcpSession(t, srv, "", ""); err == nil {
		t.Error("an anonymous session was accepted")
	}

	agent, err := mcpSession(t, srv, "agent", "Agent-pass-12345")
	if err != nil {
		t.Fatal(err)
	}

	tools, err := agent.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}

	names := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)

		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("tool %s is not marked read-only", tool.Name)
		}
	}

	for _, want := range []string{
		"search_artifacts",
		"artifact_details",
		"artifact_findings",
		"explain_download",
		"list_repositories",
		"repository_setup",
		"package_allowed",
	} {
		if !slices.Contains(names, want) {
			t.Errorf("tools %v lack %s", names, want)
		}
	}

	found, isError := toolText(t, agent, "search_artifacts", map[string]any{
		"name": "mcp-widget",
	})
	if isError || !strings.Contains(found, "mcp-open") || strings.Contains(found, "mcp-secret") {
		t.Errorf("the agent's search = %s (error %v); want mcp-open only", found, isError)
	}

	explained, isError := toolText(t, agent, "explain_download", map[string]any{
		"repository": "mcp-open",
		"path":       "tools/mcp-widget-1.0.tgz",
	})
	if isError || strings.Contains(explained, `"blocked":true`) {
		t.Errorf("explain = %s (error %v)", explained, isError)
	}

	denied, isError := toolText(t, agent, "explain_download", map[string]any{
		"repository": "mcp-secret",
		"path":       "tools/mcp-widget-1.0.tgz",
	})
	if !isError || strings.Contains(denied, "widget") {
		t.Errorf("explain of an unreadable file = %s (error %v); want an error", denied, isError)
	}

	admin, err := mcpSession(t, srv, "admin", "password")
	if err != nil {
		t.Fatal(err)
	}

	if found, _ := toolText(t, admin, "search_artifacts", map[string]any{
		"name": "mcp-widget",
	}); !strings.Contains(found, "mcp-secret") {
		t.Errorf("the administrator's search = %s", found)
	}
}
