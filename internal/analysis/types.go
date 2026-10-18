package analysis

import (
	"errors"
	"fmt"

	"github.com/Morditux/traceanvil-mcp/internal/diagnostic"
	"github.com/Morditux/traceanvil-mcp/internal/jobs"
	"github.com/Morditux/traceanvil-mcp/internal/valgrind"
)

// APISchemaVersion versions the analysis_* payloads. It changes only for an
// incompatible response or request shape.
const APISchemaVersion = 1

// ToolMemcheck is the only analysis tool implemented at J4.
const ToolMemcheck = valgrind.ProtocolTool

// State is the job lifecycle as exposed by the analysis API. It mirrors
// jobs.State and adds "unknown" for a persisted run that the current server
// instance cannot vouch for.
type State string

const (
	StateQueued    State = "queued"
	StateRunning   State = "running"
	StateCompleted State = "completed"
	StateCancelled State = "cancelled"
	StateFailed    State = "failed"
	StateTimedOut  State = "timed_out"
	StateUnknown   State = "unknown"
)

// Phase is the analysis pipeline phase. "analyzing" means the instrumented
// process has ended but parsing and report writing are still in progress.
type Phase string

const (
	PhaseQueued    Phase = "queued"
	PhaseRunning   Phase = "running"
	PhaseAnalyzing Phase = "analyzing"
	PhaseCompleted Phase = "completed"
	PhaseFailed    Phase = "failed"
	PhaseCancelled Phase = "cancelled"
	PhaseTimedOut  Phase = "timed_out"
	PhaseUnknown   Phase = "unknown"
)

// MemcheckOptions is the strict, allowlisted Memcheck option surface. Every
// field is optional; omitted fields select the documented default. Unknown
// fields are rejected by the MCP input schema.
type MemcheckOptions struct {
	LeakCheck           *string `json:"leak_check,omitempty" jsonschema:"leak check mode: none, summary or full (default full)"`
	ShowLeakKinds       *string `json:"show_leak_kinds,omitempty" jsonschema:"leak kinds to report: all, definite, possible, reachable or definite,possible (default all)"`
	TrackOrigins        *bool   `json:"track_origins,omitempty" jsonschema:"track the origin of uninitialised values (default true)"`
	NumCallers          *int    `json:"num_callers,omitempty" jsonschema:"maximum stack depth per error, 1 to 50 (default 12)"`
	SuppressionsProfile *string `json:"suppressions_profile,omitempty" jsonschema:"id of a suppression profile declared in valgrind.suppressions"`
	TraceChildren       *bool   `json:"trace_children,omitempty" jsonschema:"trace child processes; only false is supported at J4"`
}

// StartRequest is the input of analysis_start. Target arguments are a multiset
// subset of the configured target's allowed_args.
type StartRequest struct {
	TargetID  string           `json:"target_id" jsonschema:"declared target id; never a filesystem path"`
	Tool      string           `json:"tool,omitempty" jsonschema:"analysis tool; only memcheck is supported"`
	Args      []string         `json:"args,omitempty" jsonschema:"program arguments; each must be declared in the target's allowed_args"`
	TimeoutMS int              `json:"timeout_ms,omitempty" jsonschema:"optional timeout in milliseconds; may only shorten limits.timeout_seconds"`
	Options   *MemcheckOptions `json:"options,omitempty" jsonschema:"strict Memcheck options; unknown fields are rejected"`
}

// Progress reports bounded lifecycle information. The server never invents a
// completion percentage.
type Progress struct {
	ElapsedMS int64 `json:"elapsed_ms"`
}

// Metrics exposes execution counters and evidence sizes. Before an analysis is
// complete, zero counters mean "not known yet", never "no diagnostics".
type Metrics struct {
	Diagnostics      int   `json:"diagnostics"`
	Occurrences      int   `json:"occurrences"`
	Suppressed       int   `json:"suppressed"`
	StdoutBytes      int64 `json:"stdout_bytes"`
	StderrBytes      int64 `json:"stderr_bytes"`
	StdoutTruncated  bool  `json:"stdout_truncated"`
	StderrTruncated  bool  `json:"stderr_truncated"`
	OutputIncomplete bool  `json:"output_incomplete"`
	RawXMLBytes      int64 `json:"raw_xml_bytes,omitempty"`
	ReportBytes      int64 `json:"report_bytes,omitempty"`
}

// Status is the output of analysis_start, analysis_status and the basis of the
// start acknowledgement.
type Status struct {
	SchemaVersion    int                   `json:"schema_version"`
	RunID            string                `json:"run_id"`
	Tool             string                `json:"tool"`
	TargetID         string                `json:"target_id,omitempty"`
	State            State                 `json:"state"`
	Phase            Phase                 `json:"phase"`
	Result           diagnostic.Result     `json:"result,omitempty"`
	AnalysisComplete bool                  `json:"analysis_complete"`
	Program          *diagnostic.Program   `json:"program,omitempty"`
	StartedAt        string                `json:"started_at,omitempty"`
	FinishedAt       string                `json:"finished_at,omitempty"`
	Progress         Progress              `json:"progress"`
	Metrics          Metrics               `json:"metrics"`
	Artifacts        []diagnostic.Artifact `json:"artifacts"`
	ReportAvailable  bool                  `json:"report_available"`
	Warnings         []string              `json:"warnings"`
	Error            string                `json:"error,omitempty"`
}

// StatusRequest is the input of analysis_status.
type StatusRequest struct {
	RunID string `json:"run_id" jsonschema:"opaque run id returned by analysis_start"`
}

// CancelResult is the acknowledgement returned by analysis_cancel. Cancellation
// is cooperative: callers must poll analysis_status for the final state.
type CancelResult struct {
	SchemaVersion   int    `json:"schema_version"`
	RunID           string `json:"run_id"`
	CancelRequested bool   `json:"cancel_requested"`
	State           State  `json:"state"`
	Phase           Phase  `json:"phase"`
	Message         string `json:"message,omitempty"`
}

// CancelRequest is the input of analysis_cancel.
type CancelRequest struct {
	RunID string `json:"run_id" jsonschema:"opaque run id returned by analysis_start"`
}

// ReportRequest is the input of analysis_report. Filters are combined with
// logical AND; values inside kinds and severities are combined with OR.
type ReportRequest struct {
	RunID          string   `json:"run_id" jsonschema:"opaque run id returned by analysis_start"`
	Cursor         string   `json:"cursor,omitempty" jsonschema:"opaque next_cursor returned by a previous page"`
	Limit          int      `json:"limit,omitempty" jsonschema:"page size; clamped to limits.max_response_items and the server page cap"`
	Kinds          []string `json:"kinds,omitempty" jsonschema:"only diagnostics whose kind is in this list"`
	Severities     []string `json:"severities,omitempty" jsonschema:"only diagnostics whose severity is error, warning or info"`
	PathPrefix     string   `json:"path_prefix,omitempty" jsonschema:"only diagnostics with a stack frame path starting with this prefix"`
	Function       string   `json:"function,omitempty" jsonschema:"only diagnostics with a stack frame function equal to this value"`
	Fingerprint    string   `json:"fingerprint,omitempty" jsonschema:"only diagnostics with this sha256: fingerprint"`
	MinOccurrences int      `json:"min_occurrences,omitempty" jsonschema:"only diagnostics reported at least this many times"`
}

// PageInfo describes one bounded page. Matched and next_cursor refer to
// readable, filter-matching diagnostics. Diagnostics that exceed the per-record
// response bound are counted in omitted_oversized and are never silently
// treated as absent: the page carries an explicit warning and the raw artifact
// remains available.
type PageInfo struct {
	Offset                int    `json:"offset"`
	Limit                 int    `json:"limit"`
	Returned              int    `json:"returned"`
	Matched               int    `json:"matched"`
	Consumed              int    `json:"consumed"`
	HasMore               bool   `json:"has_more"`
	NextCursor            string `json:"next_cursor,omitempty"`
	ByteBudgetReached     bool   `json:"byte_budget_reached"`
	OmittedOversized      int    `json:"omitted_oversized"`
	UnreadableDiagnostics int    `json:"unreadable_diagnostics"`
	ScanIncomplete        bool   `json:"scan_incomplete"`
}

// ReportPage is the output of analysis_report: a compact summary plus one page
// of diagnostics.
type ReportPage struct {
	SchemaVersion    int                     `json:"schema_version"`
	RunID            string                  `json:"run_id"`
	Tool             diagnostic.Tool         `json:"tool"`
	Result           diagnostic.Result       `json:"result"`
	AnalysisComplete bool                    `json:"analysis_complete"`
	Program          diagnostic.Program      `json:"program"`
	Summary          diagnostic.Summary      `json:"summary"`
	Artifacts        []diagnostic.Artifact   `json:"artifacts"`
	Page             PageInfo                `json:"page"`
	Diagnostics      []diagnostic.Diagnostic `json:"diagnostics"`
	Warnings         []string                `json:"warnings"`
}

// ArtifactType identifies one allowlisted artifact of a run.
type ArtifactType string

const (
	// ArtifactRawXML is the preserved raw Valgrind XML.
	ArtifactRawXML ArtifactType = "raw_xml"
	// ArtifactAnalysisReport is the canonical analysis.json document.
	ArtifactAnalysisReport ArtifactType = "analysis_report"
	// ArtifactStdout is the bounded target/Valgrind stdout log.
	ArtifactStdout ArtifactType = "stdout"
	// ArtifactStderr is the bounded target/Valgrind stderr log.
	ArtifactStderr ArtifactType = "stderr"
	// ArtifactJobManifest is the J2 result.json manifest.
	ArtifactJobManifest ArtifactType = "job_manifest"
)

// ArtifactRequest is the input of analysis_artifact. Content is returned as a
// bounded excerpt: no absolute filesystem path is exposed.
type ArtifactRequest struct {
	RunID        string       `json:"run_id" jsonschema:"opaque run id returned by analysis_start"`
	ArtifactType ArtifactType `json:"artifact_type" jsonschema:"one of raw_xml, analysis_report, stdout, stderr, job_manifest"`
	Offset       int64        `json:"offset,omitempty" jsonschema:"byte offset in the artifact; defaults to 0"`
	MaxBytes     int          `json:"max_bytes,omitempty" jsonschema:"maximum bytes to return; clamped to the server excerpt cap"`
}

// ArtifactOutput is a bounded excerpt of one artifact.
type ArtifactOutput struct {
	SchemaVersion       int          `json:"schema_version"`
	RunID               string       `json:"run_id"`
	ArtifactType        ArtifactType `json:"artifact_type"`
	FileName            string       `json:"file_name"`
	Bytes               int64        `json:"bytes"`
	Offset              int64        `json:"offset"`
	ReturnedBytes       int          `json:"returned_bytes"`
	MaxBytes            int          `json:"max_bytes"`
	HasMore             bool         `json:"has_more"`
	NextOffset          int64        `json:"next_offset"`
	Content             string       `json:"content"`
	InvalidUTF8Replaced bool         `json:"invalid_utf8_replaced"`
}

// artifactFileName maps a logical artifact type to the file owned by the
// Memcheck pipeline. Only these names can ever be read through the API.
func artifactFileName(tool string, artifactType ArtifactType) (string, error) {
	if tool != "" && tool != ToolMemcheck {
		return "", Errorf(CodeUnknownTool, "supported tools: %s", ToolMemcheck)
	}
	switch artifactType {
	case ArtifactRawXML:
		return valgrind.RawArtifactName, nil
	case ArtifactAnalysisReport:
		return valgrind.AnalysisArtifactName, nil
	case ArtifactStdout:
		return "stdout.log", nil
	case ArtifactStderr:
		return "stderr.log", nil
	case ArtifactJobManifest:
		return jobs.ManifestName, nil
	default:
		return "", Errorf(CodeInvalidArgument,
			"artifact_type must be one of raw_xml, analysis_report, stdout, stderr, job_manifest; got %q",
			artifactType)
	}
}

// valgrindErrorOptions converts the public option surface into the internal
// allowlisted options. Omitted fields use valgrind.DefaultOptions.
func (o *MemcheckOptions) valgrindOptions() (*valgrind.Options, error) {
	if o == nil {
		return nil, nil
	}
	opts := valgrind.DefaultOptions()
	if o.LeakCheck != nil {
		if *o.LeakCheck == "" {
			return nil, Errorf(CodeInvalidArgument, "options.leak_check must not be empty")
		}
		opts.LeakCheck = valgrind.LeakCheck(*o.LeakCheck)
	}
	if o.ShowLeakKinds != nil {
		if *o.ShowLeakKinds == "" {
			return nil, Errorf(CodeInvalidArgument, "options.show_leak_kinds must not be empty")
		}
		opts.ShowLeakKinds = valgrind.ShowLeakKinds(*o.ShowLeakKinds)
	}
	if o.TrackOrigins != nil {
		opts.TrackOrigins = *o.TrackOrigins
	}
	if o.NumCallers != nil {
		if *o.NumCallers < valgrind.MinNumCallers || *o.NumCallers > valgrind.MaxNumCallers {
			return nil, Errorf(CodeInvalidArgument, "options.num_callers must be between %d and %d",
				valgrind.MinNumCallers, valgrind.MaxNumCallers)
		}
		opts.NumCallers = *o.NumCallers
	}
	if o.SuppressionsProfile != nil {
		opts.SuppressionsProfile = *o.SuppressionsProfile
	}
	if o.TraceChildren != nil {
		opts.TraceChildren = *o.TraceChildren
	}
	if err := opts.Validate(); err != nil {
		return nil, Wrap(CodeInvalidArgument, err, "%s", optionReason(err))
	}
	return &opts, nil
}

func optionReason(err error) string {
	var optionErr *valgrind.OptionError
	if errors.As(err, &optionErr) {
		return fmt.Sprintf("options.%s: %s", optionErr.Field, optionErr.Reason)
	}
	return err.Error()
}

// DiffSchemaVersion versions the diagnostics_diff payload.
const DiffSchemaVersion = 1

// DiffRequest asks for a bounded comparison of two persisted reports. The base
// run is the older/reference run; the target run is the newer/candidate run.
type DiffRequest struct {
	BaseRunID   string `json:"base_run_id" jsonschema:"opaque base run id"`
	TargetRunID string `json:"target_run_id" jsonschema:"opaque target run id"`
	Cursor      string `json:"cursor,omitempty" jsonschema:"opaque next_cursor returned by a previous diff page"`
	Limit       int    `json:"limit,omitempty" jsonschema:"page size; clamped to limits.max_response_items and the server page cap"`
}

// DiffResult is one bounded page of a diagnostic comparison. Compatibility
// changes are reported separately from item classification; when they are not
// empty, affected items are classified uncertain rather than new/resolved.
type DiffResult struct {
	SchemaVersion int                      `json:"schema_version"`
	BaseRunID     string                   `json:"base_run_id"`
	TargetRunID   string                   `json:"target_run_id"`
	Compatibility diagnostic.Compatibility `json:"compatibility"`
	Summary       diagnostic.DiffSummary   `json:"summary"`
	Page          PageInfo                 `json:"page"`
	Items         []diagnostic.DiffItem    `json:"items"`
	Warnings      []string                 `json:"warnings"`
}
