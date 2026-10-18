//go:build integration

package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type basicAuth struct {
	user, password string
}

func (b basicAuth) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.SetBasicAuth(b.user, b.password)

	return http.DefaultTransport.RoundTrip(r)
}

func mcpSession(t *testing.T, srv *httptest.Server, user, password string) *mcp.ClientSession {
	t.Helper()

	client := mcp.NewClient(&mcp.Implementation{
		Name:    "relic-test",
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
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = session.Close()
	})

	return session
}

// toolText calls a tool and returns its text and whether it is a tool error.
func toolText(t *testing.T, session *mcp.ClientSession, tool string, arguments map[string]any) (string, bool) {
	t.Helper()

	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      tool,
		Arguments: arguments,
	})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}

	var text strings.Builder

	for _, c := range result.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}

	return text.String(), result.IsError
}

func TestMCPToolsAnswerOverHTTP(t *testing.T) {
	t.Parallel()

	srv := server(t)
	admin := rest{
		t: t,
		srv: func(c call) (reply, string) {
			return do(t, srv, c)
		},
	}

	createRepository(t, srv, "mcp-seen", "LOCAL", "GENERIC", "")
	createRepository(t, srv, "mcp-hidden", "LOCAL", "GENERIC", "")
	upload(t, srv, "mcp-seen", "tools/app.bin", "seen")
	upload(t, srv, "mcp-hidden", "tools/secret.bin", "hidden")
	admin.call(
		http.MethodPut,
		"/api/v1/users/agent",
		`{"name":"agent","email":"agent@example.com","password":"Agent-pass-12345"}`,
		http.StatusOK,
	)
	admin.call(
		http.MethodPut,
		"/api/v1/permissions/mcp-agent",
		`{"name":"mcp-agent","rules":[{"resource":"RESOURCE_KIND_REPOSITORY",`+
			`"selector":{"repositories":["mcp-seen"]},`+
			`"grants":[{"user":"agent","actions":["ACTION_READ"]}]}]}`,
		http.StatusOK,
	)

	t.Run("every tool answers an administrator", func(t *testing.T) {
		t.Parallel()

		session := mcpSession(t, srv, "admin", "password")

		tools, err := session.ListTools(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}

		if len(tools.Tools) != 7 {
			t.Errorf("tools = %d, want 7", len(tools.Tools))
		}

		item := map[string]any{
			"repository": "mcp-seen",
			"path":       "tools/app.bin",
		}

		tests := []struct {
			tool      string
			arguments map[string]any
			want      string
		}{
			{
				tool: "search_artifacts",
				arguments: map[string]any{
					"name": "app.bin",
				},
				want: `"tools/app.bin"`,
			},
			{
				tool:      "artifact_details",
				arguments: item,
				want:      sha256Hex("seen"),
			},
			{
				tool:      "artifact_findings",
				arguments: item,
				want:      `"status"`,
			},
			{
				tool:      "explain_download",
				arguments: item,
				want:      "{",
			},
			{
				tool:      "list_repositories",
				arguments: map[string]any{},
				want:      `"mcp-hidden"`,
			},
			{
				tool: "repository_setup",
				arguments: map[string]any{
					"name": "mcp-seen",
				},
				want: "/mcp-seen",
			},
			{
				tool: "package_allowed",
				arguments: map[string]any{
					"purl": "pkg:npm/lodash@4.17.21",
				},
				want: "{",
			},
		}
		for _, tt := range tests {
			t.Run(tt.tool, func(t *testing.T) {
				text, isError := toolText(t, session, tt.tool, tt.arguments)
				if isError || !strings.Contains(text, tt.want) {
					t.Errorf("%s = %s (error %v), want %s in it", tt.tool, text, isError, tt.want)
				}
			})
		}
	})

	t.Run("a reader sees only what it may read", func(t *testing.T) {
		t.Parallel()

		session := mcpSession(t, srv, "agent", "Agent-pass-12345")

		repositories, _ := toolText(t, session, "list_repositories", map[string]any{})
		if !strings.Contains(repositories, "mcp-seen") || strings.Contains(repositories, "mcp-hidden") {
			t.Errorf("list_repositories = %s, want mcp-seen alone", repositories)
		}

		if text, _ := toolText(t, session, "search_artifacts", map[string]any{
			"name": ".bin",
		}); strings.Contains(text, "secret.bin") {
			t.Errorf("search_artifacts = %s, lists a file of mcp-hidden", text)
		}

		text, isError := toolText(t, session, "artifact_details", map[string]any{
			"repository": "mcp-hidden",
			"path":       "tools/secret.bin",
		})
		if !isError || !strings.HasPrefix(text, "not_found") {
			t.Errorf("artifact_details of mcp-hidden = %s (error %v), want not_found", text, isError)
		}
	})

	t.Run("an anonymous caller is refused", func(t *testing.T) {
		t.Parallel()

		resp, _ := do(t, srv, call{
			method: http.MethodPost,
			path:   "/mcp",
			body:   `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
			noAuth: true,
			headers: map[string]string{
				"Content-Type": "application/json",
				"Accept":       "application/json, text/event-stream",
			},
		})
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("anonymous POST /mcp = %d, want 401", resp.StatusCode)
		}
	})
}
