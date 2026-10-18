package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Morditux/traceanvil-mcp/internal/capabilities"
	"github.com/Morditux/traceanvil-mcp/internal/core"
)

// CoreToolNames returns the tools installed by CoreDefinitions.
func CoreToolNames() []string { return []string{"core_analyze", "core_artifact"} }

// CoreDefinitions returns the MCP tool for configured core-dump analysis.
// It is published only while GDB is available and at least one core profile is
// declared. The client supplies only a profile id, never a core/executable path.
func CoreDefinitions(service *core.Service, caps *capabilities.Registry) []ToolDefinition {
	if service == nil {
		return nil
	}
	available := func() (bool, capabilities.State, string) {
		if caps == nil {
			return false, capabilities.StateUnknown, "capability registry is unavailable"
		}
		record, ok := caps.Get(capabilities.BackendGDB)
		if !ok {
			return false, capabilities.StateUnknown, "backend.gdb capability is not registered"
		}
		if record.State != capabilities.StateAvailable {
			reason := record.Reason
			if reason == "" {
				reason = "backend.gdb is " + string(record.State)
			}
			return false, record.State, reason
		}
		if !service.HasProfiles() {
			return false, record.State, "no core profile is declared"
		}
		return true, record.State, record.Reason
	}
	return []ToolDefinition{
		{
			Name:               "core_analyze",
			RequiredCapability: capabilities.BackendGDB,
			Available:          available,
			Add: func(server *mcp.Server) {
				mcp.AddTool(server, &mcp.Tool{
					Name: "core_analyze",
					Description: "Open one configured core dump and its matching executable with fixed GDB " +
						"batch commands. The client supplies only a declared profile id; no path, debugger " +
						"command or shell fragment is accepted. Output and thread evidence are bounded.",
					Annotations: readOnlyAnnotations("Analyze core dump"),
				}, func(ctx context.Context, _ *mcp.CallToolRequest, input core.Request) (*mcp.CallToolResult, core.Report, error) {
					report, err := service.Analyze(ctx, input)
					return nil, report, err
				})
			},
		},
		{
			Name: "core_artifact",
			Add: func(server *mcp.Server) {
				mcp.AddTool(server, &mcp.Tool{
					Name: "core_artifact",
					Description: "Return a bounded UTF-8 excerpt of the preserved raw GDB output " +
						"referenced by a core_analyze evidence run id. No filesystem path is accepted " +
						"or returned.",
					Annotations: readOnlyAnnotations("Core evidence"),
				}, func(ctx context.Context, _ *mcp.CallToolRequest, input core.ArtifactRequest) (*mcp.CallToolResult, core.ArtifactOutput, error) {
					artifact, err := service.Artifact(ctx, input)
					return nil, artifact, err
				})
			},
		},
	}
}
