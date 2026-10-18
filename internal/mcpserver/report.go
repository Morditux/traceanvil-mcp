package mcpserver

import (
	"github.com/Morditux/traceanvil-mcp/internal/analysis"
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
	TimeoutSeconds       int   `json:"timeout_seconds"`
	MaxJobs              int   `json:"max_jobs"`
	MaxSessions          int   `json:"max_sessions"`
	MaxReportBytes       int64 `json:"max_report_bytes"`
	MaxResponseItems     int   `json:"max_response_items"`
	MaxPageItems         int   `json:"max_page_items"`
	DefaultArtifactBytes int   `json:"default_artifact_bytes"`
	MaxArtifactBytes     int   `json:"max_artifact_bytes"`
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
			TimeoutSeconds:       s.cfg.Limits.TimeoutSeconds,
			MaxJobs:              s.cfg.Limits.MaxJobs,
			MaxSessions:          s.cfg.Limits.MaxSessions,
			MaxReportBytes:       s.cfg.Limits.MaxReportBytes,
			MaxResponseItems:     s.cfg.Limits.MaxResponseItems,
			MaxPageItems:         min(analysis.MaxPageItems, s.cfg.Limits.MaxResponseItems),
			DefaultArtifactBytes: analysis.DefaultMaxArtifactBytes,
			MaxArtifactBytes:     analysis.MaxArtifactBytes,
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
		"Memcheck is the only implemented analysis tool; GDB and LLDB remain discovery-only (J6/J7).",
		"analysis_start is published only while backend.valgrind is available; analysis_status, analysis_report and analysis_artifact remain available for existing runs.",
		"Capability probes run at startup and periodically; tools/list_changed is emitted when tool availability changes.",
		"Diagnostics are tool-reported. analysis_complete=false means the evidence is partial; a zero count is not evidence of absence.",
		"Report pages and artifact excerpts are bounded; omitted data is reported explicitly and never treated as absent.",
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
