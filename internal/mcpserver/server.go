// Package mcpserver exposes the typed MCP surface. Handlers contain no
// business logic: they read the capability registry and tool status computed by
// this package and return structured data.
package mcpserver

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Morditux/traceanvil-mcp/internal/capabilities"
	"github.com/Morditux/traceanvil-mcp/internal/config"
	"github.com/Morditux/traceanvil-mcp/internal/version"
)

// ReportSchemaVersion versions the system_capabilities payload.
const ReportSchemaVersion = 1

// ToolDefinition describes one MCP tool and the capability it needs.
type ToolDefinition struct {
	// Name is the MCP tool name.
	Name string
	// RequiredCapability must be available for the tool to be published. An
	// empty value means the tool is always implemented and usable.
	RequiredCapability string
	// RequiredCapabilities is an any-of capability list for tools that can be
	// backed by more than one implementation, such as GDB or LLDB. It is
	// informational when Available is set.
	RequiredCapabilities []string
	// Add registers the tool on the MCP server.
	Add func(*mcp.Server)
	// Available optionally overrides RequiredCapability with a runtime check.
	// It must not execute a user program.
	Available func() (bool, capabilities.State, string)
}

// ToolStatus reports whether a tool is exposed and why.
type ToolStatus struct {
	Name                 string             `json:"name"`
	State                capabilities.State `json:"state"`
	RequiredCapability   string             `json:"required_capability,omitempty"`
	RequiredCapabilities []string           `json:"required_capabilities,omitempty"`
	Exposed              bool               `json:"exposed"`
	Reason               string             `json:"reason,omitempty"`
}

// Server owns the MCP server and the conditional tool set.
type Server struct {
	mcp    *mcp.Server
	cfg    *config.Config
	caps   *capabilities.Registry
	info   version.Info
	logger *slog.Logger

	mu     sync.Mutex
	defs   []ToolDefinition
	active map[string]bool
}

// New builds the MCP server and publishes the tools that are currently usable.
func New(cfg *config.Config, caps *capabilities.Registry, info version.Info, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	server := &Server{
		cfg:    cfg,
		caps:   caps,
		info:   info,
		logger: logger,
		active: make(map[string]bool),
	}
	server.mcp = mcp.NewServer(
		&mcp.Implementation{
			Name:        version.Binary,
			Title:       "MCP Native Diagnostics",
			Description: "Typed native dynamic-analysis diagnostics over MCP.",
			Version:     info.Version,
		},
		&mcp.ServerOptions{
			Logger: logger,
			Instructions: "Native dynamic-analysis server. system_capabilities reports build, " +
				"policy and backend state. analysis_start runs one bounded Memcheck analysis of a " +
				"declared target; analysis_status, analysis_report, analysis_artifact and " +
				"diagnostics_diff expose status, bounded evidence and versioned run comparison. CTest tools discover and run exact allowlisted tests in trusted test directories, with per-test timeouts and reports. debug_start launches a declared target under GDB or " +
				"LLDB over DAP; debug_attach is available only for an explicitly allowlisted PID. " +
				"core_analyze opens configured core profiles with fixed GDB commands. profile_callgrind " +
				"and profile_massif run declared targets with fixed native-output flags. profile_perf and " +
				"vgdb_inspect are opt-in experimental surfaces with fixed events/commands. No arbitrary " +
				"path, flag, attach target, expression evaluation, memory read or debugger command is exposed. " +
				"Backend-gated tools appear only while at least one debugger capability is available; " +
				"never interpret incomplete evidence as a clean result.",
			Capabilities: &mcp.ServerCapabilities{
				Tools: &mcp.ToolCapabilities{ListChanged: true},
			},
		},
	)

	if err := server.Register(systemCapabilitiesDefinition(server)); err != nil {
		panic(err) // programming error: duplicate built-in definition
	}
	server.SyncTools()

	return server
}

// Register adds a tool definition. Definitions are applied by SyncTools.
func (s *Server) Register(def ToolDefinition) error {
	if def.Name == "" {
		return fmt.Errorf("mcpserver: tool name must not be empty")
	}
	if def.Add == nil {
		return fmt.Errorf("mcpserver: tool %q has no registration function", def.Name)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, existing := range s.defs {
		if existing.Name == def.Name {
			return fmt.Errorf("mcpserver: tool %q is already registered", def.Name)
		}
	}
	s.defs = append(s.defs, def)
	return nil
}

// SyncTools reconciles the published tool list with capability availability.
// AddTool and RemoveTools emit tools/list_changed notifications when a session
// is connected.
func (s *Server) SyncTools() (added, removed []string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, def := range s.defs {
		allowed, _, _ := s.toolAvailability(def)
		isActive := s.active[def.Name]

		switch {
		case allowed && !isActive:
			def.Add(s.mcp)
			s.active[def.Name] = true
			added = append(added, def.Name)
		case !allowed && isActive:
			s.mcp.RemoveTools(def.Name)
			delete(s.active, def.Name)
			removed = append(removed, def.Name)
		}
	}

	if len(added) > 0 || len(removed) > 0 {
		s.logger.Info("tool list changed", "added", added, "removed", removed)
	}
	return added, removed
}

// ToolStatuses returns the state of every registered tool.
func (s *Server) ToolStatuses() []ToolStatus {
	s.mu.Lock()
	defer s.mu.Unlock()

	statuses := make([]ToolStatus, 0, len(s.defs))
	for _, def := range s.defs {
		_, state, reason := s.toolAvailability(def)
		statuses = append(statuses, ToolStatus{
			Name:                 def.Name,
			State:                state,
			RequiredCapability:   def.RequiredCapability,
			RequiredCapabilities: append([]string(nil), def.RequiredCapabilities...),
			Exposed:              s.active[def.Name],
			Reason:               reason,
		})
	}
	return statuses
}

// toolAvailability reports whether a definition may be published. Callers must
// hold s.mu or be the constructor.
func (s *Server) toolAvailability(def ToolDefinition) (bool, capabilities.State, string) {
	if def.Available != nil {
		return def.Available()
	}
	if def.RequiredCapability == "" && len(def.RequiredCapabilities) == 0 {
		return true, capabilities.StateAvailable, "implemented and enabled"
	}
	if s.caps == nil {
		return false, capabilities.StateUnknown, "capability registry is unavailable"
	}
	if len(def.RequiredCapabilities) > 0 {
		state := capabilities.StateUnknown
		reason := "no required capability is registered"
		for _, id := range def.RequiredCapabilities {
			record, ok := s.caps.Get(id)
			if !ok {
				continue
			}
			if record.State == capabilities.StateAvailable {
				return true, record.State, record.Reason
			}
			state = record.State
			reason = record.Reason
			if reason == "" {
				reason = id + " is " + string(record.State)
			}
		}
		return false, state, reason
	}

	record, ok := s.caps.Get(def.RequiredCapability)
	if !ok {
		return false, capabilities.StateUnknown, "required capability is not registered"
	}
	return record.State == capabilities.StateAvailable, record.State, record.Reason
}

// Run serves MCP on the given transport until the context is cancelled or the
// peer disconnects.
func (s *Server) Run(ctx context.Context, transport mcp.Transport) error {
	return s.mcp.Run(ctx, transport)
}

// Connect attaches an MCP session to a transport. It is primarily useful for
// in-memory tests.
func (s *Server) Connect(ctx context.Context, transport mcp.Transport) (*mcp.ServerSession, error) {
	return s.mcp.Connect(ctx, transport, nil)
}

func systemCapabilitiesDefinition(s *Server) ToolDefinition {
	return ToolDefinition{
		Name: "system_capabilities",
		Add: func(server *mcp.Server) {
			closedWorld := false
			mcp.AddTool(server, &mcp.Tool{
				Name: "system_capabilities",
				Description: "Report server build information, configured backends, " +
					"capability states, limits and exposed tools. Read-only: never launches a program.",
				Annotations: &mcp.ToolAnnotations{
					Title:          "System capabilities",
					ReadOnlyHint:   true,
					IdempotentHint: true,
					OpenWorldHint:  &closedWorld,
				},
			}, s.handleSystemCapabilities)
		},
	}
}

func (s *Server) handleSystemCapabilities(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, Report, error) {
	return nil, s.BuildReport(), nil
}
