package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Morditux/traceanvil-mcp/internal/analysis"
	"github.com/Morditux/traceanvil-mcp/internal/capabilities"
)

// AnalysisToolNames returns the tools installed by AnalysisDefinitions.
func AnalysisToolNames() []string {
	return []string{
		"analysis_start",
		"analysis_status",
		"analysis_cancel",
		"analysis_report",
		"analysis_artifact",
		"diagnostics_diff",
	}
}

// AnalysisDefinitions returns the MCP tool definitions for the analysis API.
// Handlers only delegate to analysis.Service; validation, bounds and named
// errors live in that package.
//
// analysis_start is gated on backend.valgrind availability. The evidence tools
// stay published when the backend disappears so an in-flight or persisted run
// remains queryable; they never launch a program.
func AnalysisDefinitions(service *analysis.Service) []ToolDefinition {
	if service == nil {
		return nil
	}
	return []ToolDefinition{
		{
			Name:               "analysis_start",
			RequiredCapability: capabilities.BackendValgrind,
			Add: func(server *mcp.Server) {
				mcp.AddTool(server, &mcp.Tool{
					Name: "analysis_start",
					Description: "Start one bounded Memcheck analysis of a declared target. " +
						"Launching is asynchronous: poll analysis_status. The target id is resolved " +
						"from configuration; this tool never accepts a path or a shell command.",
					Annotations: mutationAnnotations("Start analysis", true),
				}, func(ctx context.Context, _ *mcp.CallToolRequest, input analysis.StartRequest) (*mcp.CallToolResult, analysis.Status, error) {
					status, err := service.Start(ctx, input)
					return nil, status, err
				})
			},
		},
		{
			Name: "analysis_status",
			Add: func(server *mcp.Server) {
				mcp.AddTool(server, &mcp.Tool{
					Name: "analysis_status",
					Description: "Report the execution state, analysis phase and metrics of one run. " +
						"Read-only; never launches a program. Runs persisted by a previous server process " +
						"are read from their manifest and report.",
					Annotations: readOnlyAnnotations("Analysis status"),
				}, func(ctx context.Context, _ *mcp.CallToolRequest, input analysis.StatusRequest) (*mcp.CallToolResult, analysis.Status, error) {
					status, err := service.Status(ctx, input)
					return nil, status, err
				})
			},
		},
		{
			Name: "analysis_cancel",
			Add: func(server *mcp.Server) {
				mcp.AddTool(server, &mcp.Tool{
					Name: "analysis_cancel",
					Description: "Request cancellation of a running analysis. The process group receives " +
						"SIGTERM then SIGKILL. The call acknowledges the request; poll analysis_status for " +
						"the final state. Cancellation never turns partial evidence into a clean result.",
					Annotations: mutationAnnotations("Cancel analysis", true),
				}, func(ctx context.Context, _ *mcp.CallToolRequest, input analysis.CancelRequest) (*mcp.CallToolResult, analysis.CancelResult, error) {
					result, err := service.Cancel(ctx, input)
					return nil, result, err
				})
			},
		},
		{
			Name: "analysis_report",
			Add: func(server *mcp.Server) {
				mcp.AddTool(server, &mcp.Tool{
					Name: "analysis_report",
					Description: "Return a compact summary plus one bounded, paginated page of diagnostics. " +
						"Filters are optional and combined with AND. Paginate with page.next_cursor. The " +
						"response is bounded by items and bytes; diagnostics omitted for size are reported " +
						"explicitly and the raw artifact remains available.",
					Annotations: readOnlyAnnotations("Analysis report"),
				}, func(ctx context.Context, _ *mcp.CallToolRequest, input analysis.ReportRequest) (*mcp.CallToolResult, analysis.ReportPage, error) {
					page, err := service.Report(ctx, input)
					return nil, page, err
				})
			},
		},
		{
			Name: "diagnostics_diff",
			Add: func(server *mcp.Server) {
				mcp.AddTool(server, &mcp.Tool{
					Name: "diagnostics_diff",
					Description: "Compare two persisted analysis reports by versioned fingerprint and return " +
						"bounded new, resolved, persistent, changed and uncertain items. Never launches a " +
						"program. Incompatible reports fail with incompatible_runs instead of a false diff.",
					Annotations: readOnlyAnnotations("Diagnostics diff"),
				}, func(ctx context.Context, _ *mcp.CallToolRequest, input analysis.DiffRequest) (*mcp.CallToolResult, analysis.DiffResult, error) {
					result, err := service.Diff(ctx, input)
					return nil, result, err
				})
			},
		},
		{
			Name: "analysis_artifact",
			Add: func(server *mcp.Server) {
				mcp.AddTool(server, &mcp.Tool{
					Name: "analysis_artifact",
					Description: "Return a bounded excerpt of one allowlisted artifact of a run " +
						"(raw_xml, analysis_report, stdout, stderr, job_manifest). No filesystem path is " +
						"exposed and no arbitrary file can be read. Continue with next_offset.",
					Annotations: readOnlyAnnotations("Analysis artifact"),
				}, func(ctx context.Context, _ *mcp.CallToolRequest, input analysis.ArtifactRequest) (*mcp.CallToolResult, analysis.ArtifactOutput, error) {
					output, err := service.Artifact(ctx, input)
					return nil, output, err
				})
			},
		},
	}
}

func readOnlyAnnotations(title string) *mcp.ToolAnnotations {
	closedWorld := false
	return &mcp.ToolAnnotations{
		Title:          title,
		ReadOnlyHint:   true,
		IdempotentHint: true,
		OpenWorldHint:  &closedWorld,
	}
}

func mutationAnnotations(title string, destructive bool) *mcp.ToolAnnotations {
	closedWorld := false
	return &mcp.ToolAnnotations{
		Title:           title,
		ReadOnlyHint:    false,
		DestructiveHint: &destructive,
		IdempotentHint:  false,
		OpenWorldHint:   &closedWorld,
	}
}
