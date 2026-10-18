// Package mcpserver serves Relic to AI agents over the Model Context
// Protocol: read-only tools that call the Relic API as the caller, so they
// see only what the caller may read.
package mcpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"

	"connectrpc.com/connect"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
	"relic/internal/access/auth"
	"relic/internal/platform/telemetry"
)

// Path is where the server mounts the MCP endpoint.
const Path = "/mcp"

// Clients are the Relic API services the tools call.
type Clients struct {
	Artifacts    relicv1connect.ArtifactServiceClient
	Repositories relicv1connect.RepositoryServiceClient
	Scans        relicv1connect.ScanServiceClient
}

// NewClients returns the clients of the Relic API at baseURL (ending in
// /api).
func NewClients(hc connect.HTTPClient, baseURL string) Clients {
	return Clients{
		Artifacts:    relicv1connect.NewArtifactServiceClient(hc, baseURL),
		Repositories: relicv1connect.NewRepositoryServiceClient(hc, baseURL),
		Scans:        relicv1connect.NewScanServiceClient(hc, baseURL),
	}
}

// ItemRef names a file.
type ItemRef struct {
	Repository string `json:"repository" jsonschema:"the repository key"`
	Path       string `json:"path"       jsonschema:"the file's path in the repository"`
}

// SearchInput is search_artifacts' input.
type SearchInput struct {
	Name       string `json:"name"                 jsonschema:"part of the file name, matched without regard to case"`
	Repository string `json:"repository,omitempty" jsonschema:"limit the search to this repository"`
	Limit      int32  `json:"limit,omitempty"      jsonschema:"the most results per repository, 100 by default"`
}

// RepositoriesInput is list_repositories' input.
type RepositoriesInput struct{}

// RepositoryInput is repository_setup's input.
type RepositoryInput struct {
	Name string `json:"name" jsonschema:"the repository key"`
}

// PackageInput is package_allowed's input.
type PackageInput struct {
	Purl       string `json:"purl"                 jsonschema:"the package URL with its version, such as pkg:npm/lodash@4.17.21"`
	Repository string `json:"repository,omitempty" jsonschema:"the repository the package would come from"`
}

// NewServer returns an MCP server whose tools call c.
func NewServer(c Clients, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{
		Name:    "relic",
		Title:   "Relic",
		Version: version,
	}, nil)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "search_artifacts",
		Description: "Search the files stored in Relic by name, in the repositories the caller may read.",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SearchInput) (*mcp.CallToolResult, any, error) {
		return reply(ctx, "search_artifacts", func() (proto.Message, error) {
			r, err := c.Artifacts.SearchItems(ctx, connect.NewRequest(&relicv1.SearchItemsRequest{
				Name:       in.Name,
				Repository: in.Repository,
				Limit:      in.Limit,
			}))

			return message(r, err)
		})
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "artifact_details",
		Description: "A stored file's details: size, checksums, properties, the builds that made or used it, and its scan.",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ItemRef) (*mcp.CallToolResult, any, error) {
		return reply(ctx, "artifact_details", func() (proto.Message, error) {
			r, err := c.Artifacts.GetItemDetails(ctx, connect.NewRequest(&relicv1.GetItemDetailsRequest{
				Repository: in.Repository,
				Path:       in.Path,
			}))

			return message(r, err)
		})
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "artifact_findings",
		Description: "The vulnerabilities, malicious packages, secrets and misconfigurations Relic Scan found in a file.",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ItemRef) (*mcp.CallToolResult, any, error) {
		return reply(ctx, "artifact_findings", func() (proto.Message, error) {
			r, err := c.Scans.GetItemScan(ctx, connect.NewRequest(&relicv1.GetItemScanRequest{
				Repository: in.Repository,
				Path:       in.Path,
			}))

			return message(r, err)
		})
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "explain_download",
		Description: "Whether downloading a file is blocked, and by which policy, watch and rule or by the quarantine.",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ItemRef) (*mcp.CallToolResult, any, error) {
		return reply(ctx, "explain_download", func() (proto.Message, error) {
			r, err := c.Scans.ExplainDownload(ctx, connect.NewRequest(&relicv1.ExplainDownloadRequest{
				Repository: in.Repository,
				Path:       in.Path,
			}))

			return message(r, err)
		})
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_repositories",
		Description: "The repositories the caller may read: name, class (local, remote, virtual) and format.",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ RepositoriesInput) (*mcp.CallToolResult, any, error) {
		return reply(ctx, "list_repositories", func() (proto.Message, error) {
			r, err := c.Repositories.ListRepositories(ctx, connect.NewRequest(&relicv1.ListRepositoriesRequest{}))

			return message(r, err)
		})
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "repository_setup",
		Description: "The URLs a package manager or container client uses for a repository.",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in RepositoryInput) (*mcp.CallToolResult, any, error) {
		return reply(ctx, "repository_setup", func() (proto.Message, error) {
			r, err := c.Repositories.GetClientEndpoints(ctx, connect.NewRequest(&relicv1.GetClientEndpointsRequest{
				Name: in.Name,
			}))

			return message(r, err)
		})
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "package_allowed",
		Description: "Whether a package version may be added as a dependency: not known malicious, offered by the " +
			"repository (not cooling down, quarantined or blocked), and refused by no policy. Lists the version's " +
			"operational risks (end of life, deprecated, unmaintained). Ask before adding or upgrading a dependency.",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in PackageInput) (*mcp.CallToolResult, any, error) {
		return reply(ctx, "package_allowed", func() (proto.Message, error) {
			r, err := c.Scans.CheckPackage(ctx, connect.NewRequest(&relicv1.CheckPackageRequest{
				Purl:       in.Purl,
				Repository: in.Repository,
			}))

			return message(r, err)
		})
	})

	return s
}

func readOnly() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		ReadOnlyHint: true,
	}
}

func message[T any, PT interface {
	*T
	proto.Message
}](r *connect.Response[T], err error) (proto.Message, error) {
	if err != nil {
		return nil, err
	}

	return PT(r.Msg), nil
}

// reply calls fn and returns its message as JSON text, or its error as a
// tool error the agent can read.
func reply(ctx context.Context, tool string, fn func() (proto.Message, error)) (*mcp.CallToolResult, any, error) {
	ctx, span := telemetry.Tracer().Start(ctx, "mcp.CallTool")

	m, err := fn()

	telemetry.MCPToolCalled(ctx, tool, err)
	telemetry.End(span, err)

	if err != nil {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{
				&mcp.TextContent{
					Text: connect.CodeOf(err).String() + ": " + errorMessage(err),
				},
			},
		}, nil, nil
	}

	b, err := protojson.Marshal(m)
	if err != nil {
		return nil, nil, err
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{
				Text: string(b),
			},
		},
	}, nil, nil
}

func errorMessage(err error) string {
	if ce, ok := errors.AsType[*connect.Error](err); ok {
		return ce.Message()
	}

	return err.Error()
}

// Handler serves MCP over streamable HTTP. Each request's tools call api in
// process as the principal the router authenticated.
func Handler(api http.Handler, version string) http.Handler {
	return mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		hc := &http.Client{
			Transport: inProcess{
				api:       api,
				principal: auth.FromContext(r.Context()),
			},
		}

		return NewServer(NewClients(hc, "http://relic.internal/api"), version)
	}, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
		// Every request needs credentials, which a DNS rebinding page cannot supply.
		DisableLocalhostProtection: true,
	})
}

type inProcess struct {
	api       http.Handler
	principal auth.Principal
}

func (t inProcess) RoundTrip(r *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	t.api.ServeHTTP(rec, r.WithContext(auth.WithPrincipal(r.Context(), t.principal)))

	return rec.Result(), nil
}
