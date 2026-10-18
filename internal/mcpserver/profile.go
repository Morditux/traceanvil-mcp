package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Morditux/traceanvil-mcp/internal/capabilities"
	"github.com/Morditux/traceanvil-mcp/internal/profile"
)

// ProfileToolNames returns the tools installed by ProfileDefinitions.
func ProfileToolNames() []string {
	return []string{"profile_callgrind", "profile_massif", "profile_compare", "profile_artifact"}
}

// ProfileDefinitions returns the Callgrind/Massif tools. Run tools are gated
// by their native-format capability; comparison and artifact tools remain
// available for persisted evidence even when the profiler disappears.
func ProfileDefinitions(service *profile.Service, caps *capabilities.Registry) []ToolDefinition {
	if service == nil {
		return nil
	}
	available := func(id string) func() (bool, capabilities.State, string) {
		return func() (bool, capabilities.State, string) {
			if caps == nil {
				return false, capabilities.StateUnknown, "capability registry is unavailable"
			}
			base, ok := caps.Get(capabilities.BackendValgrind)
			if !ok {
				return false, capabilities.StateUnknown, "backend.valgrind capability is not registered"
			}
			if base.State != capabilities.StateAvailable {
				reason := base.Reason
				if reason == "" {
					reason = "backend.valgrind is " + string(base.State)
				}
				return false, base.State, reason
			}
			record, ok := caps.Get(id)
			if !ok {
				return false, capabilities.StateUnknown, id + " capability is not registered"
			}
			if record.State != capabilities.StateAvailable {
				reason := record.Reason
				if reason == "" {
					reason = id + " is " + string(record.State)
				}
				return false, record.State, reason
			}
			if !service.HasUsableTarget() {
				return false, record.State, "no declared target is currently usable"
			}
			return true, record.State, record.Reason
		}
	}
	runTool := func(name string, tool profile.Tool, capability string, description string) ToolDefinition {
		return ToolDefinition{
			Name:               name,
			RequiredCapability: capability,
			Available:          available(capability),
			Add: func(server *mcp.Server) {
				mcp.AddTool(server, &mcp.Tool{
					Name: name, Description: description,
					Annotations: mutationAnnotations("Run "+string(tool)+" profile", true),
				}, func(ctx context.Context, _ *mcp.CallToolRequest, input profile.ToolRequest) (*mcp.CallToolResult, profile.Report, error) {
					report, err := service.Run(ctx, tool, input)
					return nil, report, err
				})
			},
		}
	}
	return []ToolDefinition{
		runTool("profile_callgrind", profile.ToolCallgrind, capabilities.BackendValgrindCallgrind,
			"Run one declared target under Valgrind Callgrind with fixed flags, preserve "+
				"callgrind.out and return normalized instruction/event totals and top self-cost "+
				"functions. Instruction counts are deterministic for the same binary, input and "+
				"tool configuration; do not infer wall-clock time."),
		runTool("profile_massif", profile.ToolMassif, capabilities.BackendValgrindMassif,
			"Run one declared target under Valgrind Massif with fixed flags, preserve massif.out "+
				"and return peak heap, extra heap, stack bytes, snapshot count and bounded samples. "+
				"Heap-tree text is preserved in the raw artifact, not interpreted as a leak claim."),
		{
			Name: "profile_compare",
			Add: func(server *mcp.Server) {
				mcp.AddTool(server, &mcp.Tool{
					Name: "profile_compare",
					Description: "Compare two persisted profiles of the same tool. Callgrind event " +
						"sets/order and Massif time units must match; otherwise comparison is refused. " +
						"Deltas are descriptive, not a statistical significance test or a regression claim.",
					Annotations: readOnlyAnnotations("Compare profiles"),
				}, func(ctx context.Context, _ *mcp.CallToolRequest, input profile.CompareRequest) (*mcp.CallToolResult, profile.Comparison, error) {
					comparison, err := service.Compare(ctx, input.BaseRunID, input.TargetRunID)
					return nil, comparison, err
				})
			},
		},
		{
			Name: "profile_artifact",
			Add: func(server *mcp.Server) {
				mcp.AddTool(server, &mcp.Tool{
					Name: "profile_artifact",
					Description: "Return a bounded UTF-8 excerpt of profile.json, callgrind.out or " +
						"massif.out for a persisted profile run. No filesystem path is accepted or returned.",
					Annotations: readOnlyAnnotations("Profile artifact"),
				}, func(ctx context.Context, _ *mcp.CallToolRequest, input profile.ArtifactRequest) (*mcp.CallToolResult, profile.ArtifactOutput, error) {
					artifact, err := service.Artifact(ctx, input)
					return nil, artifact, err
				})
			},
		},
	}
}
