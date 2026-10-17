package telemetry

import (
	"context"
	"slices"
)

// PromQL: sum by (tool, result) (rate(relic_mcp_tool_calls_total[5m]))
var mcpToolCalls = counterVec(
	"mcp_tool_calls_total",
	"MCP tool calls by tool and result.",
	"tool",
	"result",
)

var mcpTools = []string{
	"search_artifacts",
	"artifact_details",
	"artifact_findings",
	"explain_download",
	"list_repositories",
	"repository_setup",
	"package_allowed",
}

// MCPToolCalled counts a call of an MCP tool.
func MCPToolCalled(_ context.Context, tool string, err error) {
	if !slices.Contains(mcpTools, tool) {
		tool = Other
	}

	mcpToolCalls.WithLabelValues(tool, Result(err)).Inc()
}

// PromQL: sum by (decision) (increase(relic_registry_approvals_total[1d]))
var registryApprovals = counterVec(
	"registry_approvals_total",
	"Agent registry approvals by decision (approved, revoked).",
	"decision",
)

// RegistryApproval counts an approval or a revocation of an agent registry
// entry.
func RegistryApproval(approved bool) {
	decision := "revoked"
	if approved {
		decision = "approved"
	}

	registryApprovals.WithLabelValues(decision).Inc()
}
