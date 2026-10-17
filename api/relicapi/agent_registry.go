package relicapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/access/auth"
	"relic/internal/artifacts/formats/agentregistry"
	"relic/internal/artifacts/repository"
)

// MCPRegistryPath serves the approved MCP servers of mcp repositories in the
// MCP registry API: <MCPRegistryPath><repository>/v0/servers.
const MCPRegistryPath = "/api/mcp/"

type agentRegistryServer struct {
	d Dependencies
}

func registryEntryMessage(e agentregistry.Entry) *relicv1.RegistryEntry {
	return &relicv1.RegistryEntry{
		Repository:  e.Repository,
		Path:        e.Path,
		Format:      formatMessage(e.Kind),
		Name:        e.Name,
		Version:     e.Version,
		Description: e.Description,
		Sha256:      e.SHA256,
		Document:    string(e.Doc),
		Updated:     timestamppb.New(e.Updated),
		Approved:    e.IsApproved(),
		ApprovedBy:  e.ApprovedBy,
		ApprovedAt:  ts(e.Approved),
	}
}

// registryRepository checks the caller may read repo, an mcp or skills
// repository.
func registryRepository(ctx context.Context, d Dependencies, repo string) error {
	if err := can(ctx, d, repo, "", auth.ActionRead); err != nil {
		return err
	}

	r, err := d.Repositories.GetRepository(ctx, repo)
	if err != nil {
		return err
	}

	if r.Format != repository.FormatMCP && r.Format != repository.FormatSkills {
		return connect.NewError(
			connect.CodeInvalidArgument,
			errors.New(repo+" is not an mcp or skills repository"),
		)
	}

	return nil
}

func (s *agentRegistryServer) ListRegistryEntries(
	ctx context.Context,
	req *connect.Request[relicv1.ListRegistryEntriesRequest],
) (*connect.Response[relicv1.ListRegistryEntriesResponse], error) {
	repo := req.Msg.GetRepository()
	if err := registryRepository(ctx, s.d, repo); err != nil {
		return nil, err
	}

	p := auth.FromContext(ctx)

	list, err := s.d.AgentRegistry.Entries(ctx, repo, !p.Admin && !p.Viewer)
	if err != nil {
		return nil, err
	}

	out := &relicv1.ListRegistryEntriesResponse{
		Entries: make([]*relicv1.RegistryEntry, 0, len(list)),
	}
	for _, e := range list {
		out.Entries = append(out.Entries, registryEntryMessage(e))
	}

	return connect.NewResponse(out), nil
}

func (s *agentRegistryServer) ApproveRegistryEntry(
	ctx context.Context,
	req *connect.Request[relicv1.ApproveRegistryEntryRequest],
) (*connect.Response[relicv1.ApproveRegistryEntryResponse], error) {
	m := req.Msg
	if err := s.d.AgentRegistry.Approve(
		ctx,
		m.GetRepository(),
		cleanPath(m.GetPath()),
		m.GetSha256(),
		auth.FromContext(ctx).Name(),
	); err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.ApproveRegistryEntryResponse{}), nil
}

func (s *agentRegistryServer) RevokeRegistryApproval(
	ctx context.Context,
	req *connect.Request[relicv1.RevokeRegistryApprovalRequest],
) (*connect.Response[relicv1.RevokeRegistryApprovalResponse], error) {
	m := req.Msg
	if err := s.d.AgentRegistry.Revoke(ctx, m.GetRepository(), cleanPath(m.GetPath())); err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.RevokeRegistryApprovalResponse{}), nil
}

type registryServer struct {
	Server json.RawMessage `json:"server"`
	Meta   map[string]any  `json:"_meta"`
}

func registryServerEntry(e agentregistry.Entry, latest bool) registryServer {
	return registryServer{
		Server: e.Doc,
		Meta: map[string]any{
			"io.modelcontextprotocol.registry/official": map[string]any{
				"status":      "active",
				"publishedAt": e.Updated.Format(time.RFC3339),
				"updatedAt":   e.Updated.Format(time.RFC3339),
				"isLatest":    latest,
			},
		},
	}
}

// mcpRegistry serves GET <repo>/v0/servers, <repo>/v0/servers/<name>/versions
// and <repo>/v0/servers/<name>/versions/<version> (or latest).
func (d Dependencies) mcpRegistry(w http.ResponseWriter, r *http.Request) {
	repo, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, MCPRegistryPath), "/")

	rest, ok := strings.CutPrefix(rest, "v0/servers")
	if !ok || r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}

	if err := registryRepository(r.Context(), d, repo); err != nil {
		writeHTTPError(w, d.logged(r, err))
		return
	}

	list, err := d.AgentRegistry.Entries(r.Context(), repo, true)
	if err != nil {
		writeHTTPError(w, d.logged(r, err))
		return
	}

	latest := map[string]bool{}
	search := strings.ToLower(r.URL.Query().Get("search"))
	servers := make([]registryServer, 0, len(list))

	for _, e := range list {
		if e.Kind != repository.FormatMCP {
			continue
		}

		isLatest := !latest[e.Name]
		latest[e.Name] = true

		if search == "" || strings.Contains(strings.ToLower(e.Name), search) {
			servers = append(servers, registryServerEntry(e, isLatest))
		}
	}

	name, version, hasVersion := strings.Cut(strings.TrimPrefix(rest, "/"), "/versions")
	version = strings.TrimPrefix(version, "/")

	switch {
	case rest == "" || rest == "/":
		writeJSON(w, map[string]any{
			"servers": servers,
			"metadata": map[string]any{
				"count": len(servers),
			},
		})
	case hasVersion:
		var matched []registryServer

		newest := true

		for _, e := range list {
			if e.Kind != repository.FormatMCP || e.Name != name {
				continue
			}

			isLatest := newest
			newest = false

			if version == "" || version == e.Version || (version == "latest" && isLatest) {
				matched = append(matched, registryServerEntry(e, isLatest))
			} else if version == "latest" {
				break
			}
		}

		if len(matched) == 0 {
			http.NotFound(w, r)
			return
		}

		if version == "" {
			writeJSON(w, map[string]any{
				"servers": matched,
				"metadata": map[string]any{
					"count": len(matched),
				},
			})

			return
		}

		writeJSON(w, matched[0])
	default:
		http.NotFound(w, r)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
