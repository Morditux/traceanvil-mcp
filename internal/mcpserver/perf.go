package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Morditux/traceanvil-mcp/internal/capabilities"
	"github.com/Morditux/traceanvil-mcp/internal/perf"
)

// PerfToolNames returns the experimental perf tools.
func PerfToolNames() []string { return []string{"profile_perf", "perf_artifact"} }

// PerfDefinitions returns the experimental perf stat surface. profile_perf is
// published only after the capability probe proves local permissions; the
// event list is fixed by the service.
func PerfDefinitions(service *perf.Service, caps *capabilities.Registry) []ToolDefinition {
	if service == nil {
		return nil
	}
	available := func() (bool, capabilities.State, string) {
		if caps == nil {
			return false, capabilities.StateUnknown, "capability registry is unavailable"
		}
		record, ok := caps.Get(capabilities.BackendPerf)
		if !ok {
			return false, capabilities.StateUnknown, "backend.perf capability is not registered"
		}
		if record.State != capabilities.StateAvailable {
			reason := record.Reason
			if reason == "" {
				reason = "backend.perf is " + string(record.State)
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
			Name: "profile_perf", RequiredCapability: capabilities.BackendPerf, Available: available,
			Add: func(server *mcp.Server) {
				mcp.AddTool(server, &mcp.Tool{
					Name: "profile_perf",
					Description: "EXPERIMENTAL: run one declared target under Linux perf stat " +
						"with the fixed events task-clock, cycles and instructions. The client " +
						"cannot add events or perf flags. The tool is hidden unless the host " +
						"permission probe succeeds; it never changes perf_event_paranoid, uses " +
						"sudo or escalates privileges. Results are experimental and incomplete " +
						"when counters are unsupported.",
					Annotations: mutationAnnotations("Run experimental perf profile", true),
				}, func(ctx context.Context, _ *mcp.CallToolRequest, input perf.Request) (*mcp.CallToolResult, perf.Report, error) {
					report, err := service.Run(ctx, input)
					return nil, report, err
				})
			},
		},
		{
			Name: "perf_artifact",
			Add: func(server *mcp.Server) {
				mcp.AddTool(server, &mcp.Tool{
					Name: "perf_artifact",
					Description: "Return a bounded excerpt of preserved experimental perf evidence " +
						"(perf.json, stdout.log or stderr.log). No filesystem path is accepted.",
					Annotations: readOnlyAnnotations("Perf artifact"),
				}, func(ctx context.Context, _ *mcp.CallToolRequest, input perf.ArtifactRequest) (*mcp.CallToolResult, perf.ArtifactOutput, error) {
					artifact, err := service.Artifact(ctx, input)
					return nil, artifact, err
				})
			},
		},
	}
}
