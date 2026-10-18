package sanitizer

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Morditux/traceanvil-mcp/internal/diagnostic"
)

// BuildInput carries the parsed log and execution outcome.
type BuildInput struct {
	Tool        Tool
	RunID       string
	Roots       []string
	Issues      []Issue
	ParseResult Result
	ParseErr    error
	Program     diagnostic.Program
	Artifact    diagnostic.Artifact
}

// BuildReport converts sanitizer log issues into the canonical report. It
// never claims a clean result when the log, parsing or process state is
// incomplete, and it warns that a clean concurrency run is not proof of absence.
func BuildReport(in BuildInput) diagnostic.Report {
	report := diagnostic.Report{
		SchemaVersion:    diagnostic.SchemaVersion,
		RunID:            in.RunID,
		Tool:             diagnostic.Tool{Name: string(in.Tool)},
		AnalysisComplete: false,
		Program:          in.Program,
		Options: map[string]any{
			"tool": string(in.Tool),
		},
		Metadata: diagnostic.Metadata{
			OS:                   osName(),
			Arch:                 archName(),
			WorkspaceRoots:       append([]string(nil), in.Roots...),
			FingerprintAlgorithm: diagnostic.FingerprintAlgorithmV2,
			ParserVersion:        in.Tool.ParserName(),
		},
		Artifacts:   []diagnostic.Artifact{},
		Diagnostics: []diagnostic.Diagnostic{},
		Warnings:    []string{},
	}
	if in.Artifact.Name != "" {
		report.Artifacts = append(report.Artifacts, in.Artifact)
	}
	complete := true
	if in.ParseErr != nil {
		report.Warnings = append(report.Warnings, "sanitizer log parse failed: "+in.ParseErr.Error())
		complete = false
	}
	if in.ParseResult.Truncated || in.ParseResult.Incomplete {
		complete = false
	}
	report.Warnings = append(report.Warnings, in.ParseResult.Warnings...)
	if in.Artifact.Truncated {
		report.Warnings = append(report.Warnings, "sanitizer log was truncated at the artifact bound")
		complete = false
	}
	if in.Program.TimedOut || in.Program.Canceled {
		report.Warnings = append(report.Warnings, "sanitizer run did not complete normally")
		complete = false
	}
	if in.Program.Signal != 0 {
		report.Warnings = append(report.Warnings, fmt.Sprintf(
			"instrumented process terminated by signal %d", in.Program.Signal))
	}

	issues := in.Issues
	if len(issues) == 0 {
		issues = in.ParseResult.Issues
	}
	for index, issue := range issues {
		kind := strings.TrimSpace(issue.Kind)
		if kind == "" {
			kind = "SanitizerIssue"
		}
		stack := normalizeFrames(issue.Stack, in.Roots)
		previous := normalizeFrames(issue.PreviousStack, in.Roots)
		message := strings.TrimSpace(issue.Message)
		if message == "" {
			message = kind
		}
		fingerprintFrames := sanitizerFingerprintFrames(stack, previous)
		diagnosticItem := diagnostic.Diagnostic{
			ID:                   diagnosticID(in.RunID, index),
			Fingerprint:          diagnostic.FingerprintV2(in.Tool.Origin(), kind, fingerprintFrames),
			FingerprintAlgorithm: diagnostic.FingerprintAlgorithmV2,
			Origin:               in.Tool.Origin(),
			Kind:                 kind,
			Severity:             issue.Severity,
			Message:              message,
			Location:             diagnostic.SelectLocation(stack),
			Stack:                stack,
			Occurrences:          1,
			LeakedBytes:          issue.LeakedBytes,
			LeakedBlocks:         issue.LeakedBlocks,
			Evidence: diagnostic.Evidence{
				Artifact: in.Artifact.Name, RecordID: fmt.Sprintf("issue-%d", index), RecordIndex: index,
			},
			Confidence: diagnostic.ConfidenceToolReported,
		}
		if len(previous) > 0 {
			diagnosticItem.ContextStacks = append(diagnosticItem.ContextStacks,
				diagnostic.ContextStack{Label: "previous access", Frames: previous})
		}
		report.Diagnostics = append(report.Diagnostics, diagnosticItem)
	}
	report.Summary = diagnostic.BuildSummary(report.Diagnostics, nil, in.ParseResult.Truncated)

	switch {
	case len(report.Diagnostics) > 0:
		report.Result = diagnostic.ResultErrorsFound
		complete = complete && !in.ParseResult.Truncated
	case complete && in.Program.ExitCode == 0:
		report.Result = diagnostic.ResultNoErrorsObserved
		report.Warnings = append(report.Warnings,
			"the target is declared sanitizer-instrumented and reported no sanitizer errors; this is not proof that no such error exists")
	case complete:
		report.Result = diagnostic.ResultIncomplete
		report.Warnings = append(report.Warnings,
			"the instrumented process exited with a non-zero status but no sanitizer diagnostic was recognized")
		complete = false
	default:
		report.Result = diagnostic.ResultIncomplete
	}
	report.AnalysisComplete = complete
	return report
}

func normalizeFrames(frames []diagnostic.Frame, roots []string) []diagnostic.Frame {
	result := make([]diagnostic.Frame, 0, len(frames))
	for _, frame := range frames {
		frame.Path = diagnostic.NormalizePath(frame.Path, roots)
		frame.Function = strings.TrimSpace(frame.Function)
		frame.Object = strings.TrimSpace(frame.Object)
		if frame.Function == "" && frame.Path == "" && frame.Object != "" {
			// FingerprintV2 ignores frames without a function or path. Keep an
			// object-only frame distinguishable without retaining the randomized
			// instruction address.
			frame.Function = "object:" + stableObject(frame.Object)
		}
		result = append(result, frame)
	}
	return result
}

func stableObject(object string) string {
	base := filepath.Base(strings.TrimSpace(object))
	if base == "." || base == string(filepath.Separator) {
		return strings.TrimSpace(object)
	}
	return base
}

func sanitizerFingerprintFrames(stack, previous []diagnostic.Frame) []diagnostic.Frame {
	if len(previous) == 0 {
		return stack
	}
	// FingerprintV2 has a 16-frame cap. Reserve room for a separator and for
	// the previous-access stack so a deep current stack cannot hide it.
	const currentCap = 8
	const previousCap = 7
	frames := make([]diagnostic.Frame, 0, currentCap+1+previousCap)
	frames = append(frames, capFrames(stack, currentCap)...)
	frames = append(frames, diagnostic.Frame{Function: "__previous_access__"})
	frames = append(frames, capFrames(previous, previousCap)...)
	return frames
}

func capFrames(frames []diagnostic.Frame, limit int) []diagnostic.Frame {
	if len(frames) <= limit {
		return frames
	}
	return frames[:limit]
}

func diagnosticID(runID string, index int) string {
	suffix := strings.TrimPrefix(runID, "run_")
	if suffix == "" {
		suffix = "unbound"
	}
	return fmt.Sprintf("diag_%s_%d", suffix, index)
}

func osName() string   { return runtime.GOOS }
func archName() string { return runtime.GOARCH }
