package mcpserver

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Morditux/traceanvil-mcp/internal/capabilities"
	"github.com/Morditux/traceanvil-mcp/internal/config"
	"github.com/Morditux/traceanvil-mcp/internal/version"
)

func testConfig() *config.Config {
	return &config.Config{
		SchemaVersion:  1,
		WorkspaceRoots: []string{"/tmp/ws"},
		ReportDir:      "/tmp/reports",
		AllowedTargets: []config.Target{},
		Backends:       config.Backends{Valgrind: true, GDB: true},
		Policy:         config.Policy{Profile: config.ProfileSafeDefault},
		Limits: config.Limits{
			TimeoutSeconds:   120,
			MaxJobs:          2,
			MaxSessions:      2,
			MaxReportBytes:   1 << 20,
			MaxResponseItems: 100,
		},
	}
}

func connect(t *testing.T, server *Server, options *mcp.ClientOptions) (*mcp.ClientSession, context.Context) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport)
	if err != nil {
		t.Fatalf("server.Connect() error = %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, options)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect() error = %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })

	return clientSession, ctx
}

func listToolNames(t *testing.T, session *mcp.ClientSession, ctx context.Context) []string {
	t.Helper()

	result, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	names := make([]string, 0, len(result.Tools))
	for _, tool := range result.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestSystemCapabilitiesTool(t *testing.T) {
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

	server := New(testConfig(), registry, version.Current(), nil)
	session, ctx := connect(t, server, nil)

	init := session.InitializeResult()
	if init == nil || init.Capabilities == nil || init.Capabilities.Tools == nil {
		t.Fatal("InitializeResult has no tools capability")
	}
	if !init.Capabilities.Tools.ListChanged {
		t.Error("tools.listChanged = false, want true")
	}

	names := listToolNames(t, session, ctx)
	if len(names) != 1 || names[0] != "system_capabilities" {
		t.Fatalf("tools = %v, want [system_capabilities]", names)
	}

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "system_capabilities",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("CallTool() IsError = true: %+v", result.Content)
	}
	if len(result.Content) == 0 {
		t.Error("Content is empty, want the JSON representation of the report")
	}

	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	var report Report
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}

	if report.SchemaVersion != ReportSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", report.SchemaVersion, ReportSchemaVersion)
	}
	if report.Server.Name != version.Binary {
		t.Errorf("Server.Name = %q, want %q", report.Server.Name, version.Binary)
	}
	if report.Policy.Profile != config.ProfileSafeDefault {
		t.Errorf("Policy.Profile = %q, want %q", report.Policy.Profile, config.ProfileSafeDefault)
	}
	if len(report.Capabilities) != 1 || report.Capabilities[0].ID != capabilities.BackendValgrind {
		t.Errorf("Capabilities = %+v, want one valgrind record", report.Capabilities)
	}
	if len(report.Tools) != 1 || report.Tools[0].Name != "system_capabilities" || !report.Tools[0].Exposed {
		t.Errorf("Tools = %+v, want exposed system_capabilities", report.Tools)
	}
	if len(report.Warnings) == 0 {
		t.Error("Warnings is empty, want at least one limitation warning")
	}
}

func TestConditionalToolPublicationAndNotification(t *testing.T) {
	registry := capabilities.NewRegistry(nil, time.Second)

	notifications := make(chan struct{}, 4)
	options := &mcp.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {
			select {
			case notifications <- struct{}{}:
			default:
			}
		},
	}

	server := New(testConfig(), registry, version.Current(), nil)
	if err := server.Register(ToolDefinition{
		Name:               "fake_tool",
		RequiredCapability: "fake.capability",
		Add: func(m *mcp.Server) {
			mcp.AddTool(m, &mcp.Tool{
				Name:        "fake_tool",
				Description: "test-only tool",
			}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, struct{}, error) {
				return nil, struct{}{}, nil
			})
		},
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	session, ctx := connect(t, server, options)

	if names := listToolNames(t, session, ctx); contains(names, "fake_tool") {
		t.Fatalf("tools = %v, fake_tool must not be exposed before its capability is available", names)
	}

	if err := registry.Set(capabilities.Capability{
		ID:    "fake.capability",
		Kind:  "backend",
		State: capabilities.StateAvailable,
	}); err != nil {
		t.Fatal(err)
	}
	if status := toolStatus(server.ToolStatuses(), "fake_tool"); status.Exposed {
		t.Fatal("fake_tool is reported as exposed before SyncTools publishes it")
	}
	added, removed := server.SyncTools()
	if len(added) != 1 || added[0] != "fake_tool" || len(removed) != 0 {
		t.Fatalf("SyncTools() = added %v, removed %v; want added [fake_tool]", added, removed)
	}
	waitForNotification(t, notifications)

	if names := listToolNames(t, session, ctx); !contains(names, "fake_tool") {
		t.Fatalf("tools = %v, want fake_tool after capability becomes available", names)
	}

	if err := registry.Set(capabilities.Capability{
		ID:     "fake.capability",
		Kind:   "backend",
		State:  capabilities.StateUnavailable,
		Reason: "gone",
	}); err != nil {
		t.Fatal(err)
	}
	added, removed = server.SyncTools()
	if len(removed) != 1 || removed[0] != "fake_tool" || len(added) != 0 {
		t.Fatalf("SyncTools() = added %v, removed %v; want removed [fake_tool]", added, removed)
	}
	waitForNotification(t, notifications)

	if names := listToolNames(t, session, ctx); contains(names, "fake_tool") {
		t.Fatalf("tools = %v, fake_tool must not remain after its capability becomes unavailable", names)
	}
}

func TestRegisterRejectsInvalidDefinitions(t *testing.T) {
	server := New(testConfig(), capabilities.NewRegistry(nil, time.Second), version.Current(), nil)

	if err := server.Register(ToolDefinition{}); err == nil {
		t.Error("Register() with empty name error = nil, want an error")
	}
	if err := server.Register(ToolDefinition{Name: "no-add"}); err == nil {
		t.Error("Register() without Add error = nil, want an error")
	}
	if err := server.Register(ToolDefinition{
		Name: "system_capabilities",
		Add:  func(*mcp.Server) {},
	}); err == nil {
		t.Error("Register() duplicate error = nil, want an error")
	}
}

func TestBuildReportWithNilRegistry(t *testing.T) {
	server := New(testConfig(), nil, version.Current(), nil)

	report := server.BuildReport()
	if report.Capabilities == nil {
		t.Error("Capabilities is nil, want an empty slice")
	}
	if len(report.Tools) != 1 || report.Tools[0].Name != "system_capabilities" {
		t.Errorf("Tools = %+v, want system_capabilities", report.Tools)
	}
}

func toolStatus(statuses []ToolStatus, name string) ToolStatus {
	for _, status := range statuses {
		if status.Name == name {
			return status
		}
	}
	return ToolStatus{}
}

func waitForNotification(t *testing.T, notifications <-chan struct{}) {
	t.Helper()

	select {
	case <-notifications:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for tools/list_changed notification")
	}
}
