package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Morditux/traceanvil-mcp/internal/analysis"
	"github.com/Morditux/traceanvil-mcp/internal/artifacts"
	"github.com/Morditux/traceanvil-mcp/internal/capabilities"
	"github.com/Morditux/traceanvil-mcp/internal/diagnostic"
	"github.com/Morditux/traceanvil-mcp/internal/jobs"
	"github.com/Morditux/traceanvil-mcp/internal/valgrind"
	"github.com/Morditux/traceanvil-mcp/internal/version"
)

type stubRun struct {
	mu       sync.Mutex
	id       string
	snapshot valgrind.AnalysisSnapshot
	job      jobs.Snapshot
	canceled bool
}

func (r *stubRun) ID() string { return r.id }

func (r *stubRun) Snapshot() valgrind.AnalysisSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshot
}

func (r *stubRun) JobSnapshot() jobs.Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.job
}

func (r *stubRun) Cancel() {
	r.mu.Lock()
	r.canceled = true
	r.mu.Unlock()
}

type stubBackend struct {
	mu   sync.Mutex
	runs map[string]*stubRun
}

func newStubBackend() *stubBackend {
	return &stubBackend{runs: make(map[string]*stubRun)}
}

func (b *stubBackend) Start(_ context.Context, req valgrind.Request) (analysis.Run, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := "run_000000000000000000000000000000aa"
	now := time.Now().UTC()
	run := &stubRun{
		id: id,
		snapshot: valgrind.AnalysisSnapshot{
			RunID: id,
			Phase: valgrind.PhaseRunning,
		},
		job: jobs.Snapshot{
			SchemaVersion: jobs.SchemaVersion,
			RunID:         id,
			TargetID:      req.TargetID,
			State:         jobs.StateRunning,
			CreatedAt:     now,
			StartedAt:     &now,
		},
	}
	b.runs[id] = run
	return run, nil
}

func (b *stubBackend) Get(runID string) (analysis.Run, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	run, ok := b.runs[runID]
	if !ok {
		return nil, false
	}
	return run, true
}

func newAnalysisServer(t *testing.T, registry *capabilities.Registry) (*Server, *analysis.Service, *stubBackend) {
	t.Helper()

	store, err := artifacts.NewStore(t.TempDir(), 8<<20)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	backend := newStubBackend()
	service, err := analysis.New(analysis.Options{
		Store:          store,
		Backend:        backend,
		BackendEnabled: true,
		BackendAvailable: func() (bool, string) {
			record, ok := registry.Get(capabilities.BackendValgrind)
			if !ok {
				return false, "valgrind capability is not registered"
			}
			return record.State == capabilities.StateAvailable, record.Reason
		},
	})
	if err != nil {
		t.Fatalf("analysis.New() error = %v", err)
	}
	t.Cleanup(service.Close)

	server := New(testConfig(), registry, version.Current(), nil)
	for _, definition := range AnalysisDefinitions(service) {
		if err := server.Register(definition); err != nil {
			t.Fatalf("Register(%s) error = %v", definition.Name, err)
		}
	}
	server.SyncTools()
	return server, service, backend
}

func TestAnalysisToolPublicationFollowsBackendCapability(t *testing.T) {
	registry := capabilities.NewRegistry(nil, time.Second)
	if err := registry.Set(capabilities.Capability{
		ID:      capabilities.BackendValgrind,
		Kind:    "backend",
		Backend: "valgrind",
		State:   capabilities.StateUnavailable,
		Reason:  "not installed",
	}); err != nil {
		t.Fatal(err)
	}
	server, _, _ := newAnalysisServer(t, registry)

	notifications := make(chan struct{}, 8)
	options := &mcp.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {
			select {
			case notifications <- struct{}{}:
			default:
			}
		},
	}
	session, ctx := connect(t, server, options)

	names := listToolNames(t, session, ctx)
	for _, want := range AnalysisToolNames() {
		exposed := contains(names, want)
		if want == "analysis_start" && exposed {
			t.Errorf("analysis_start is exposed while backend.valgrind is unavailable")
		}
		if want != "analysis_start" && !exposed {
			t.Errorf("tools = %v, missing evidence tool %q", names, want)
		}
	}

	if err := registry.Set(capabilities.Capability{
		ID:      capabilities.BackendValgrind,
		Kind:    "backend",
		Backend: "valgrind",
		State:   capabilities.StateAvailable,
		Path:    "/usr/bin/valgrind",
	}); err != nil {
		t.Fatal(err)
	}
	added, _ := server.SyncTools()
	if len(added) != 1 || added[0] != "analysis_start" {
		t.Fatalf("SyncTools() added = %v, want [analysis_start]", added)
	}
	waitForNotification(t, notifications)
	if names := listToolNames(t, session, ctx); !contains(names, "analysis_start") {
		t.Errorf("tools = %v, analysis_start must appear", names)
	}

	if err := registry.Set(capabilities.Capability{
		ID:      capabilities.BackendValgrind,
		Kind:    "backend",
		Backend: "valgrind",
		State:   capabilities.StateUnavailable,
		Reason:  "removed",
	}); err != nil {
		t.Fatal(err)
	}
	_, removed := server.SyncTools()
	if len(removed) != 1 || removed[0] != "analysis_start" {
		t.Fatalf("SyncTools() removed = %v, want [analysis_start]", removed)
	}
	waitForNotification(t, notifications)
	names = listToolNames(t, session, ctx)
	if contains(names, "analysis_start") {
		t.Errorf("tools = %v, analysis_start must disappear", names)
	}
	for _, want := range AnalysisToolNames() {
		if want != "analysis_start" && !contains(names, want) {
			t.Errorf("tools = %v, evidence tool %q must stay published", names, want)
		}
	}
}

func TestAnalysisToolSchemasAreStrict(t *testing.T) {
	registry := capabilities.NewRegistry(nil, time.Second)
	if err := registry.Set(capabilities.Capability{
		ID:      capabilities.BackendValgrind,
		Kind:    "backend",
		Backend: "valgrind",
		State:   capabilities.StateAvailable,
		Path:    "/usr/bin/valgrind",
	}); err != nil {
		t.Fatal(err)
	}
	server, _, _ := newAnalysisServer(t, registry)
	session, ctx := connect(t, server, nil)

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	found := make(map[string]*mcp.Tool)
	for _, tool := range tools.Tools {
		found[tool.Name] = tool
	}

	start, ok := found["analysis_start"]
	if !ok {
		t.Fatal("analysis_start is not published")
	}
	if start.Annotations == nil || start.Annotations.ReadOnlyHint {
		t.Error("analysis_start must not be marked read-only")
	}
	schema, ok := start.InputSchema.(map[string]any)
	if !ok {
		t.Fatalf("analysis_start input schema type = %T", start.InputSchema)
	}
	if additional, present := schema["additionalProperties"]; !present || additional != false {
		t.Errorf("analysis_start additionalProperties = %v, want false", additional)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("analysis_start properties = %T", schema["properties"])
	}
	for _, field := range []string{"target_id", "tool", "args", "timeout_ms", "options"} {
		if _, ok := properties[field]; !ok {
			t.Errorf("analysis_start schema is missing %q", field)
		}
	}
	options, ok := properties["options"].(map[string]any)
	if !ok {
		t.Fatalf("analysis_start options schema = %T", properties["options"])
	}
	if additional, present := options["additionalProperties"]; !present || additional != false {
		t.Errorf("analysis_start options additionalProperties = %v, want false", additional)
	}

	report, ok := found["analysis_report"]
	if !ok {
		t.Fatal("analysis_report is not published")
	}
	if report.Annotations == nil || !report.Annotations.ReadOnlyHint {
		t.Error("analysis_report must be marked read-only")
	}
	if report.OutputSchema == nil {
		t.Error("analysis_report has no output schema")
	}

	diff, ok := found["diagnostics_diff"]
	if !ok {
		t.Fatal("diagnostics_diff is not published")
	}
	if diff.Annotations == nil || !diff.Annotations.ReadOnlyHint {
		t.Error("diagnostics_diff must be marked read-only")
	}
	diffSchema, ok := diff.InputSchema.(map[string]any)
	if !ok {
		t.Fatalf("diagnostics_diff input schema type = %T", diff.InputSchema)
	}
	diffProperties, ok := diffSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("diagnostics_diff properties = %T", diffSchema["properties"])
	}
	for _, field := range []string{"base_run_id", "target_run_id", "cursor", "limit"} {
		if _, ok := diffProperties[field]; !ok {
			t.Errorf("diagnostics_diff schema is missing %q", field)
		}
	}
	if diff.OutputSchema == nil {
		t.Error("diagnostics_diff has no output schema")
	}
}

func TestAnalysisToolsCallAndNamedErrors(t *testing.T) {
	registry := capabilities.NewRegistry(nil, time.Second)
	if err := registry.Set(capabilities.Capability{
		ID:      capabilities.BackendValgrind,
		Kind:    "backend",
		Backend: "valgrind",
		State:   capabilities.StateAvailable,
		Path:    "/usr/bin/valgrind",
	}); err != nil {
		t.Fatal(err)
	}
	server, service, backend := newAnalysisServer(t, registry)
	_ = service
	session, ctx := connect(t, server, nil)

	startResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "analysis_start",
		Arguments: map[string]any{"target_id": "fixture"},
	})
	if err != nil {
		t.Fatalf("CallTool(analysis_start) error = %v", err)
	}
	if startResult.IsError {
		t.Fatalf("analysis_start returned an error: %s", toolText(startResult))
	}
	var started analysis.Status
	decodeCall(t, startResult, &started)
	if started.RunID != "run_000000000000000000000000000000aa" {
		t.Errorf("run_id = %q", started.RunID)
	}

	statusResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "analysis_status",
		Arguments: map[string]any{"run_id": started.RunID},
	})
	if err != nil {
		t.Fatalf("CallTool(analysis_status) error = %v", err)
	}
	if statusResult.IsError {
		t.Fatalf("analysis_status returned an error: %s", toolText(statusResult))
	}

	cancelResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "analysis_cancel",
		Arguments: map[string]any{"run_id": started.RunID},
	})
	if err != nil {
		t.Fatalf("CallTool(analysis_cancel) error = %v", err)
	}
	if cancelResult.IsError {
		t.Fatalf("analysis_cancel returned an error: %s", toolText(cancelResult))
	}
	var canceled analysis.CancelResult
	decodeCall(t, cancelResult, &canceled)
	if !canceled.CancelRequested {
		t.Error("cancel_requested = false for a running run")
	}
	run, _ := backend.Get(started.RunID)
	if !run.(*stubRun).canceled {
		t.Error("stub run was not canceled")
	}

	tests := []struct {
		name      string
		tool      string
		arguments map[string]any
		wantText  string
	}{
		{
			name:      "unknown run",
			tool:      "analysis_status",
			arguments: map[string]any{"run_id": "run_ffffffffffffffffffffffffffffffff"},
			wantText:  "run_not_found",
		},
		{
			name:      "invalid artifact type",
			tool:      "analysis_artifact",
			arguments: map[string]any{"run_id": started.RunID, "artifact_type": "secrets"},
			wantText:  "invalid_argument",
		},
		{
			name:      "unknown input field",
			tool:      "analysis_report",
			arguments: map[string]any{"run_id": started.RunID, "bogus": true},
			wantText:  "bogus",
		},
		{
			name:      "missing run id",
			tool:      "analysis_status",
			arguments: map[string]any{},
			wantText:  "run_id",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := session.CallTool(ctx, &mcp.CallToolParams{
				Name:      test.tool,
				Arguments: test.arguments,
			})
			if err != nil {
				t.Fatalf("CallTool() error = %v", err)
			}
			if !result.IsError {
				t.Fatalf("CallTool() succeeded, want an error")
			}
			text := toolText(result)
			if !strings.Contains(text, test.wantText) {
				if test.wantText != "bogus" || !strings.Contains(text, "additional") {
					t.Errorf("error = %q, want %q", text, test.wantText)
				}
			}
		})
	}
}

func TestAnalysisReportAndArtifactThroughMCP(t *testing.T) {
	registry := capabilities.NewRegistry(nil, time.Second)

	// The MCP layer does not need the backend to read persisted evidence.
	store, err := artifacts.NewStore(t.TempDir(), 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	runDir, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	report := diagnostic.Report{
		SchemaVersion:    diagnostic.SchemaVersion,
		RunID:            runDir.ID(),
		Tool:             diagnostic.Tool{Name: "valgrind", Path: "/usr/bin/valgrind", Version: "valgrind-3.26.0"},
		Result:           diagnostic.ResultErrorsFound,
		AnalysisComplete: true,
		Program:          diagnostic.Program{TargetID: "fixture", Args: []string{}, ExitCode: 42},
		Diagnostics: []diagnostic.Diagnostic{{
			ID:                   "diag_0",
			Fingerprint:          "sha256:" + strings.Repeat("ab", 32),
			FingerprintAlgorithm: diagnostic.FingerprintAlgorithmV1,
			Origin:               valgrind.Origin,
			Kind:                 "InvalidRead",
			Severity:             diagnostic.SeverityError,
			Message:              "Invalid read of size 4",
			Occurrences:          1,
			Evidence:             diagnostic.Evidence{Artifact: valgrind.RawArtifactName},
			Confidence:           diagnostic.ConfidenceToolReported,
		}},
		Artifacts: []diagnostic.Artifact{},
		Warnings:  []string{},
	}
	report.Summary = diagnostic.BuildSummary(report.Diagnostics, nil, false)
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := runDir.WriteFileAtomic(valgrind.AnalysisArtifactName, data); err != nil {
		t.Fatal(err)
	}
	if err := runDir.WriteFileAtomic(valgrind.RawArtifactName, []byte("<valgrindoutput></valgrindoutput>")); err != nil {
		t.Fatal(err)
	}

	server2, _, _ := newAnalysisServerWithStore(t, registry, store)
	session2, ctx2 := connect(t, server2, nil)

	reportResult, err := session2.CallTool(ctx2, &mcp.CallToolParams{
		Name:      "analysis_report",
		Arguments: map[string]any{"run_id": runDir.ID()},
	})
	if err != nil {
		t.Fatalf("CallTool(analysis_report) error = %v", err)
	}
	if reportResult.IsError {
		t.Fatalf("analysis_report returned an error: %s", toolText(reportResult))
	}
	var page analysis.ReportPage
	decodeCall(t, reportResult, &page)
	if page.Page.Matched != 1 || len(page.Diagnostics) != 1 {
		t.Errorf("page = %+v", page.Page)
	}

	artifactResult, err := session2.CallTool(ctx2, &mcp.CallToolParams{
		Name: "analysis_artifact",
		Arguments: map[string]any{
			"run_id":        runDir.ID(),
			"artifact_type": "raw_xml",
		},
	})
	if err != nil {
		t.Fatalf("CallTool(analysis_artifact) error = %v", err)
	}
	if artifactResult.IsError {
		t.Fatalf("analysis_artifact returned an error: %s", toolText(artifactResult))
	}
	var artifact analysis.ArtifactOutput
	decodeCall(t, artifactResult, &artifact)
	if !strings.Contains(artifact.Content, "<valgrindoutput>") {
		t.Errorf("artifact content = %q", artifact.Content)
	}

}

func newAnalysisServerWithStore(t *testing.T, registry *capabilities.Registry, store *artifacts.Store) (*Server, *analysis.Service, *stubBackend) {
	t.Helper()
	backend := newStubBackend()
	service, err := analysis.New(analysis.Options{
		Store:          store,
		Backend:        backend,
		BackendEnabled: true,
	})
	if err != nil {
		t.Fatalf("analysis.New() error = %v", err)
	}
	t.Cleanup(service.Close)
	server := New(testConfig(), registry, version.Current(), nil)
	for _, definition := range AnalysisDefinitions(service) {
		if err := server.Register(definition); err != nil {
			t.Fatalf("Register(%s) error = %v", definition.Name, err)
		}
	}
	server.SyncTools()
	return server, service, backend
}

func decodeCall(t *testing.T, result *mcp.CallToolResult, target any) {
	t.Helper()
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("unmarshal structured content: %v (%s)", err, raw)
	}
}

func toolText(result *mcp.CallToolResult) string {
	var builder strings.Builder
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			builder.WriteString(text.Text)
		}
	}
	return builder.String()
}

func mcpDiffReport(runID string, id, fingerprint string) diagnostic.Report {
	return diagnostic.Report{
		SchemaVersion:    diagnostic.SchemaVersion,
		RunID:            runID,
		Tool:             diagnostic.Tool{Name: "valgrind", Path: "/usr/bin/valgrind", Version: "valgrind-3.26.0"},
		Result:           diagnostic.ResultErrorsFound,
		AnalysisComplete: true,
		Program: diagnostic.Program{
			TargetID: "fixture", Path: "/workspace/fixture",
			SHA256: "sha256:" + strings.Repeat("cd", 32), Args: []string{}, ExitCode: 42,
		},
		Metadata: diagnostic.Metadata{
			OS: "linux", Arch: "amd64", WorkspaceRoots: []string{"/workspace"},
			FingerprintAlgorithm: diagnostic.FingerprintAlgorithmV2, ParserVersion: "1",
		},
		Options: map[string]any{"leak_check": "full"},
		Summary: diagnostic.BuildSummary([]diagnostic.Diagnostic{{
			ID: id, Fingerprint: "sha256:" + strings.Repeat(fingerprint, 32),
			FingerprintAlgorithm: diagnostic.FingerprintAlgorithmV2, Origin: valgrind.Origin,
			Kind: "InvalidRead", Severity: diagnostic.SeverityError, Message: "Invalid read",
			Occurrences: 1, Evidence: diagnostic.Evidence{Artifact: valgrind.RawArtifactName},
			Confidence: diagnostic.ConfidenceToolReported,
		}}, nil, false),
		Diagnostics: []diagnostic.Diagnostic{{
			ID: id, Fingerprint: "sha256:" + strings.Repeat(fingerprint, 32),
			FingerprintAlgorithm: diagnostic.FingerprintAlgorithmV2, Origin: valgrind.Origin,
			Kind: "InvalidRead", Severity: diagnostic.SeverityError, Message: "Invalid read",
			Occurrences: 1, Evidence: diagnostic.Evidence{Artifact: valgrind.RawArtifactName},
			Confidence: diagnostic.ConfidenceToolReported,
		}},
		Artifacts: []diagnostic.Artifact{},
		Warnings:  []string{},
	}
}

func writeMCPDiffReport(t *testing.T, store *artifacts.Store, report diagnostic.Report) string {
	t.Helper()
	runDir, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	report.RunID = runDir.ID()
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := runDir.WriteFileAtomic(valgrind.AnalysisArtifactName, data); err != nil {
		t.Fatal(err)
	}
	return runDir.ID()
}

func TestDiagnosticsDiffTool(t *testing.T) {
	store, err := artifacts.NewStore(t.TempDir(), 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	backend := newStubBackend()
	service, err := analysis.New(analysis.Options{Store: store, Backend: backend, BackendEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	registry := capabilities.NewRegistry(nil, time.Second)
	if err := registry.Set(capabilities.Capability{
		ID: capabilities.BackendValgrind, Kind: "backend", Backend: "valgrind",
		State: capabilities.StateAvailable, Path: "/usr/bin/valgrind",
	}); err != nil {
		t.Fatal(err)
	}
	server := New(testConfig(), registry, version.Current(), nil)
	for _, definition := range AnalysisDefinitions(service) {
		if err := server.Register(definition); err != nil {
			t.Fatal(err)
		}
	}
	server.SyncTools()
	baseID := writeMCPDiffReport(t, store, mcpDiffReport("", "base", "aa"))
	targetID := writeMCPDiffReport(t, store, mcpDiffReport("", "target", "aa"))
	session, ctx := connect(t, server, nil)
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "diagnostics_diff",
		Arguments: map[string]any{
			"base_run_id": baseID, "target_run_id": targetID,
		},
	})
	if err != nil || result.IsError {
		t.Fatalf("diagnostics_diff = %+v, %v", result, err)
	}
	var diff analysis.DiffResult
	decodeCall(t, result, &diff)
	if diff.Summary.Persistent != 1 || diff.Summary.New != 0 || diff.Summary.Resolved != 0 {
		t.Fatalf("diff = %+v", diff)
	}
	if len(diff.Items) != 1 || diff.Items[0].Change != diagnostic.ChangePersistent {
		t.Fatalf("items = %+v", diff.Items)
	}
}
