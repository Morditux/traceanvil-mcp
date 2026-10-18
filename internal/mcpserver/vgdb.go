package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Morditux/traceanvil-mcp/internal/capabilities"
	"github.com/Morditux/traceanvil-mcp/internal/vgdb"
)

// VgdbToolNames returns the experimental vgdb tools.
func VgdbToolNames() []string { return []string{"vgdb_inspect", "vgdb_artifact"} }

// VgdbDefinitions returns the fixed-command vgdb prototype. No tool accepts a
// monitor command; only the service's three read-only commands are sent.
func VgdbDefinitions(service *vgdb.Service, caps *capabilities.Registry) []ToolDefinition {
	if service == nil {
		return nil
	}
	available := func() (bool, capabilities.State, string) {
		if caps == nil {
			return false, capabilities.StateUnknown, "capability registry is unavailable"
		}
		base, ok := caps.Get(capabilities.BackendValgrind)
		if !ok || base.State != capabilities.StateAvailable {
			return false, capabilities.StateUnknown, "backend.valgrind is unavailable"
		}
		record, ok := caps.Get(capabilities.BackendVgdb)
		if !ok {
			return false, capabilities.StateUnknown, "backend.vgdb capability is not registered"
		}
		if record.State != capabilities.StateAvailable {
			reason := record.Reason
			if reason == "" {
				reason = "backend.vgdb is " + string(record.State)
			}
			return false, record.State, reason
		}
		if !service.HasUsableTarget() {
			return false, record.State, "no declared target is currently usable"
		}
		return true, record.State, record.Reason
	}
	return []ToolDefinition{
		{
			Name: "vgdb_inspect", RequiredCapability: capabilities.BackendVgdb, Available: available,
			Add: func(server *mcp.Server) {
				mcp.AddTool(server, &mcp.Tool{
					Name: "vgdb_inspect",
					Description: "EXPERIMENTAL: start Valgrind with the vgdb server on a declared " +
						"target and send only the fixed read-only commands v.info gdbserver_status, " +
						"v.info exectxt and v.info scheduler. There is no monitor-command passthrough; " +
						"cleanup uses v.kill and the job process group. Results are experimental.",
					Annotations: mutationAnnotations("Run experimental vgdb inspection", true),
				}, func(ctx context.Context, _ *mcp.CallToolRequest, input vgdb.Request) (*mcp.CallToolResult, vgdb.Report, error) {
					report, err := service.Run(ctx, input)
					return nil, report, err
				})
			},
		},
		{
			Name: "vgdb_artifact",
			Add: func(server *mcp.Server) {
				mcp.AddTool(server, &mcp.Tool{
					Name: "vgdb_artifact",
					Description: "Return a bounded excerpt of preserved experimental vgdb evidence " +
						"(vgdb.json, stdout.log or stderr.log). No filesystem path is accepted.",
					Annotations: readOnlyAnnotations("vgdb artifact"),
				}, func(ctx context.Context, _ *mcp.CallToolRequest, input vgdb.ArtifactRequest) (*mcp.CallToolResult, vgdb.ArtifactOutput, error) {
					artifact, err := service.Artifact(ctx, input)
					return nil, artifact, err
				})
			},
		},
	}
}
