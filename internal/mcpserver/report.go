package mcpserver

import (
	"github.com/Morditux/traceanvil-mcp/internal/capabilities"
	"github.com/Morditux/traceanvil-mcp/internal/config"
	"github.com/Morditux/traceanvil-mcp/internal/version"
)

// Report is the structured payload of system_capabilities.
type Report struct {
	SchemaVersion int                       `json:"schema_version"`
	Server        ServerInfo                `json:"server"`
	Policy        PolicyInfo                `json:"policy"`
	Limits        LimitsInfo                `json:"limits"`
	Capabilities  []capabilities.Capability `json:"capabilities"`
	Tools         []ToolStatus              `json:"tools"`
	Warnings      []string                  `json:"warnings"`
}

// ServerInfo is the build identity of the running server.
type ServerInfo struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Date      string `json:"date"`
	GoVersion string `json:"go_version"`
	Modified  bool   `json:"modified"`
}

// PolicyInfo is the effective trust policy summary.
type PolicyInfo struct {
	Profile  string `json:"profile"`
	Attach   bool   `json:"attach"`
	Evaluate bool   `json:"evaluate"`
}

// LimitsInfo is the effective bounded-execution configuration.
type LimitsInfo struct {
	TimeoutSeconds   int   `json:"timeout_seconds"`
	MaxJobs          int   `json:"max_jobs"`
	MaxSessions      int   `json:"max_sessions"`
	MaxReportBytes   int64 `json:"max_report_bytes"`
	MaxResponseItems int   `json:"max_response_items"`
}

// BuildReport assembles the current capability view. It reads the registry
// snapshot and computes tool exposure; it never probes or executes anything.
func (s *Server) BuildReport() Report {
	report := Report{
		SchemaVersion: ReportSchemaVersion,
		Server: ServerInfo{
			Name:      version.Binary,
			Version:   s.info.Version,
			Commit:    s.info.Commit,
			Date:      s.info.Date,
			GoVersion: s.info.GoVersion,
			Modified:  s.info.Modified,
		},
		Warnings: s.warnings(),
	}

	if s.cfg != nil {
		report.Policy = PolicyInfo{
			Profile:  s.cfg.Policy.Profile,
			Attach:   s.cfg.Policy.Attach,
			Evaluate: s.cfg.Policy.Evaluate,
		}
		report.Limits = LimitsInfo{
			TimeoutSeconds:   s.cfg.Limits.TimeoutSeconds,
			MaxJobs:          s.cfg.Limits.MaxJobs,
			MaxSessions:      s.cfg.Limits.MaxSessions,
			MaxReportBytes:   s.cfg.Limits.MaxReportBytes,
			MaxResponseItems: s.cfg.Limits.MaxResponseItems,
		}
	}

	if s.caps != nil {
		report.Capabilities = s.caps.Snapshot()
	} else {
		report.Capabilities = []capabilities.Capability{}
	}

	report.Tools = s.ToolStatuses()
	if report.Tools == nil {
		report.Tools = []ToolStatus{}
	}
	return report
}

func (s *Server) warnings() []string {
	warnings := []string{
		"Only system_capabilities is implemented at this milestone; analysis tools arrive with J4.",
		"Valgrind is functionally probed for Memcheck XML support; GDB and LLDB remain discovery-only (J6/J7).",
		"Capability probes run at startup only; restart the server after installing or removing a backend.",
	}

	if s.cfg == nil {
		return warnings
	}
	if s.cfg.Policy.Profile == config.ProfileElevatedLocal {
		warnings = append(warnings, "elevated-local is configured, but elevated features are not implemented.")
	}
	if !s.cfg.Backends.Valgrind && !s.cfg.Backends.GDB && !s.cfg.Backends.LLDB {
		warnings = append(warnings, "All external backends are disabled by configuration.")
	}
	if len(s.cfg.AllowedTargets) == 0 {
		warnings = append(warnings, "No analysis target is configured.")
	}
	return warnings
}
