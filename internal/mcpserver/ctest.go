package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Morditux/traceanvil-mcp/internal/capabilities"
	"github.com/Morditux/traceanvil-mcp/internal/ctest"
)

// CTestToolNames returns the tools installed by CTestDefinitions.
func CTestToolNames() []string {
	return []string{
		"ctest_discover",
		"ctest_run",
		"ctest_status",
		"ctest_cancel",
		"ctest_report",
		"ctest_artifact",
	}
}

// CTestDefinitions returns MCP tool definitions for trusted CTest projects.
// Discovery and execution require the CTest capability and at least one usable
// project directory; evidence tools remain available for persisted runs.
func CTestDefinitions(service *ctest.Service, caps *capabilities.Registry) []ToolDefinition {
	if service == nil {
		return nil
	}
	available := func() (bool, capabilities.State, string) {
		if caps == nil {
			return false, capabilities.StateUnknown, "capability registry is unavailable"
		}
		record, ok := caps.Get(capabilities.BackendCTest)
		if !ok {
			return false, capabilities.StateUnknown, "backend.ctest capability is not registered"
		}
		if record.State != capabilities.StateAvailable {
			reason := record.Reason
			if reason == "" {
				reason = "backend.ctest is " + string(record.State)
			}
			return false, record.State, reason
		}
		if !service.HasUsableProject() {
			return false, record.State, "no usable CTest project directory is available"
		}
		return true, record.State, record.Reason
	}
	return []ToolDefinition{
		{
			Name:               "ctest_discover",
			RequiredCapability: capabilities.BackendCTest,
			Available:          available,
			Add: func(server *mcp.Server) {
				mcp.AddTool(server, &mcp.Tool{
					Name: "ctest_discover",
					Description: "List the exact tests declared for one trusted CTest project. " +
						"Runs ctest --show-only=json-v1, never executes a test, and filters the " +
						"result to the project's allowed_tests.",
					Annotations: readOnlyAnnotations("Discover CTest tests"),
				}, func(ctx context.Context, _ *mcp.CallToolRequest, input ctest.DiscoverRequest) (*mcp.CallToolResult, ctest.Discovery, error) {
					discovery, err := service.Discover(ctx, input)
					return nil, discovery, err
				})
			},
		},
		{
			Name:               "ctest_run",
			RequiredCapability: capabilities.BackendCTest,
			Available:          available,
			Add: func(server *mcp.Server) {
				mcp.AddTool(server, &mcp.Tool{
					Name: "ctest_run",
					Description: "Start one bounded CTest execution for exact allowed tests or a " +
						"configured profile. The server never builds the project, never accepts a " +
						"shell command or a client regex, and applies an overall timeout plus a " +
						"per-test CTest timeout. Poll ctest_status for the result.",
					Annotations: mutationAnnotations("Run CTest tests", true),
				}, func(ctx context.Context, _ *mcp.CallToolRequest, input ctest.RunRequest) (*mcp.CallToolResult, ctest.Status, error) {
					status, err := service.Start(ctx, input)
					return nil, status, err
				})
			},
		},
		{
			Name: "ctest_status",
			Add: func(server *mcp.Server) {
				mcp.AddTool(server, &mcp.Tool{
					Name: "ctest_status",
					Description: "Report lifecycle, aggregate result and per-test counts for one " +
						"CTest run. Read-only; never launches CTest.",
					Annotations: readOnlyAnnotations("CTest status"),
				}, func(ctx context.Context, _ *mcp.CallToolRequest, input ctest.StatusRequest) (*mcp.CallToolResult, ctest.Status, error) {
					status, err := service.Status(ctx, input)
					return nil, status, err
				})
			},
		},
		{
			Name: "ctest_cancel",
			Add: func(server *mcp.Server) {
				mcp.AddTool(server, &mcp.Tool{
					Name: "ctest_cancel",
					Description: "Cancel one CTest run owned by this server process. Persisted " +
						"runs are not active and are returned as not canceled.",
					Annotations: mutationAnnotations("Cancel CTest run", false),
				}, func(ctx context.Context, _ *mcp.CallToolRequest, input ctest.CancelRequest) (*mcp.CallToolResult, ctest.CancelResult, error) {
					result, err := service.Cancel(ctx, input)
					return nil, result, err
				})
			},
		},
		{
			Name: "ctest_report",
			Add: func(server *mcp.Server) {
				mcp.AddTool(server, &mcp.Tool{
					Name: "ctest_report",
					Description: "Return a bounded per-test CTest report with aggregate counts and " +
						"raw artifact references. Missing or unparsable evidence is incomplete, never " +
						"a passed run.",
					Annotations: readOnlyAnnotations("CTest report"),
				}, func(ctx context.Context, _ *mcp.CallToolRequest, input ctest.ReportRequest) (*mcp.CallToolResult, ctest.Report, error) {
					report, err := service.Report(ctx, input)
					return nil, report, err
				})
			},
		},
		{
			Name: "ctest_artifact",
			Add: func(server *mcp.Server) {
				mcp.AddTool(server, &mcp.Tool{
					Name: "ctest_artifact",
					Description: "Return a bounded UTF-8 excerpt of one allowlisted CTest artifact: " +
						"junit, stdout, stderr or report. No filesystem path is accepted or returned.",
					Annotations: readOnlyAnnotations("CTest artifact"),
				}, func(ctx context.Context, _ *mcp.CallToolRequest, input ctest.ArtifactRequest) (*mcp.CallToolResult, ctest.ArtifactOutput, error) {
					artifact, err := service.Artifact(ctx, input)
					return nil, artifact, err
				})
			},
		},
	}
}
