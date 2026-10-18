package valgrind

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Morditux/traceanvil-mcp/internal/diagnostic"
)

// Origin identifies diagnostics produced by this pipeline.
const Origin = "valgrind-memcheck"

// RawArtifactName is the preserved Valgrind XML document.
const RawArtifactName = "memcheck.xml"

// AnalysisArtifactName is the normalized report.
const AnalysisArtifactName = "analysis.json"

// BuildInput carries everything needed to turn one XML document into a
// canonical report. Classification (errors_found, incomplete, …) is applied by
// the service after it has the job outcome.
type BuildInput struct {
	RunID       string
	ToolPath    string
	ToolVersion string
	Roots       []string
	Options     Options
	XML         *XMLResult
	ParseErr    error
	Artifacts   []diagnostic.Artifact
	Program     diagnostic.Program
}

// BuildReport converts raw Valgrind XML into the canonical diagnostic model.
// It never upgrades partial evidence: parse errors, truncation, dropped records
// and inconsistent summaries all leave AnalysisComplete false.
func BuildReport(in BuildInput) diagnostic.Report {
	report := diagnostic.Report{
		SchemaVersion:    diagnostic.SchemaVersion,
		RunID:            in.RunID,
		Tool:             diagnostic.Tool{Name: "valgrind", Path: in.ToolPath, Version: in.ToolVersion},
		AnalysisComplete: false,
		Program:          in.Program,
		Options:          in.Options.Map(),
		Metadata: diagnostic.Metadata{
			OS:                   osName(),
			Arch:                 archName(),
			WorkspaceRoots:       append([]string(nil), in.Roots...),
			FingerprintAlgorithm: diagnostic.FingerprintAlgorithmV1,
			ParserVersion:        ParserVersion,
		},
		Artifacts:   append([]diagnostic.Artifact(nil), in.Artifacts...),
		Diagnostics: []diagnostic.Diagnostic{},
		Warnings:    []string{},
	}
	if report.Artifacts == nil {
		report.Artifacts = []diagnostic.Artifact{}
	}
	if in.XML == nil {
		report.Warnings = append(report.Warnings, "no Valgrind XML document was produced")
		report.Summary = diagnostic.BuildSummary(nil, nil, true)
		return report
	}

	xmlResult := in.XML
	report.Warnings = append(report.Warnings, xmlResult.Warnings...)

	counts := make(map[string]int, len(xmlResult.ErrorCounts))
	for _, count := range xmlResult.ErrorCounts {
		if count.Unique == "" {
			continue
		}
		if count.Count > 0 {
			counts[count.Unique] = count.Count
		}
	}

	for index, raw := range xmlResult.Errors {
		occurrences := 1
		if count, ok := counts[raw.Unique]; ok {
			occurrences = count
		}

		stack := convertFrames(raw.Stack, in.Roots)
		contexts := make([]diagnostic.ContextStack, 0, len(raw.Contexts))
		for _, context := range raw.Contexts {
			contexts = append(contexts, diagnostic.ContextStack{
				Label:  strings.TrimSpace(context.Label),
				Frames: convertFrames(context.Frames, in.Roots),
			})
		}
		origin := convertFrames(raw.Origin, in.Roots)

		kind := strings.TrimSpace(raw.Kind)
		if kind == "" {
			kind = "unknown"
		}
		message := strings.TrimSpace(raw.XWhat)
		if message == "" {
			message = strings.TrimSpace(raw.What)
		}
		if message == "" {
			message = kind
		}

		report.Diagnostics = append(report.Diagnostics, diagnostic.Diagnostic{
			ID:                   diagnosticID(in.RunID, index),
			Fingerprint:          diagnostic.FingerprintV1(Origin, kind, stack),
			FingerprintAlgorithm: diagnostic.FingerprintAlgorithmV1,
			Origin:               Origin,
			Kind:                 kind,
			Severity:             severityForKind(kind),
			Message:              message,
			Location:             diagnostic.SelectLocation(stack),
			Stack:                stack,
			ContextStacks:        contexts,
			OriginStack:          origin,
			Occurrences:          occurrences,
			LeakedBytes:          raw.LeakedBytes,
			LeakedBlocks:         raw.LeakedBlocks,
			Evidence: diagnostic.Evidence{
				Artifact:    RawArtifactName,
				RecordID:    strings.TrimSpace(raw.Unique),
				RecordIndex: index,
				RawKind:     raw.Kind,
			},
			Confidence: diagnostic.ConfidenceToolReported,
		})
	}

	suppressions := make([]diagnostic.Suppression, 0, len(xmlResult.Suppressions))
	for _, suppression := range xmlResult.Suppressions {
		if suppression.Name == "" {
			continue
		}
		suppressions = append(suppressions, diagnostic.Suppression{
			Name:   suppression.Name,
			Count:  suppression.Count,
			Unique: suppression.Unique,
		})
	}
	report.Suppressions = suppressions
	report.Summary = diagnostic.BuildSummary(report.Diagnostics, suppressions, xmlResult.Truncated || xmlResult.DroppedDiagnostics > 0)

	complete := true
	if in.ParseErr != nil {
		report.Warnings = append(report.Warnings, in.ParseErr.Error())
		complete = false
	}
	if !xmlResult.Complete {
		complete = false
	}
	if xmlResult.Truncated {
		complete = false
	}
	if xmlResult.DroppedDiagnostics > 0 {
		report.Warnings = append(report.Warnings,
			fmt.Sprintf("%d diagnostics were dropped after the collection limit", xmlResult.DroppedDiagnostics))
		complete = false
	}
	if xmlResult.FinalState != "FINISHED" {
		if xmlResult.FinalState == "" {
			report.Warnings = append(report.Warnings, "Valgrind did not report a final state")
		} else {
			report.Warnings = append(report.Warnings, "Valgrind final state is "+xmlResult.FinalState)
		}
		complete = false
	}
	if xmlResult.ErrorSummary == nil {
		report.Warnings = append(report.Warnings, "Valgrind error_summary is missing")
		complete = false
	} else {
		if xmlResult.ErrorSummary.Errors != report.Summary.Total {
			report.Warnings = append(report.Warnings,
				fmt.Sprintf("Valgrind reported %d errors but %d occurrences were parsed",
					xmlResult.ErrorSummary.Errors, report.Summary.Total))
			complete = false
		}
		if xmlResult.ErrorSummary.Suppressed != report.Summary.Suppressed {
			report.Warnings = append(report.Warnings,
				fmt.Sprintf("Valgrind reported %d suppressed errors but %d suppression counts were parsed",
					xmlResult.ErrorSummary.Suppressed, report.Summary.Suppressed))
			complete = false
		}
	}
	if report.Summary.Suppressed > 0 {
		report.Warnings = append(report.Warnings,
			"suppressed errors are not evidence of absence and are never counted as resolved")
	}
	report.AnalysisComplete = complete
	return report
}

func diagnosticID(runID string, index int) string {
	suffix := strings.TrimPrefix(runID, "run_")
	if suffix == "" {
		suffix = "unbound"
	}
	return fmt.Sprintf("diag_%s_%d", suffix, index)
}

func convertFrames(frames []XMLFrame, roots []string) []diagnostic.Frame {
	converted := make([]diagnostic.Frame, 0, len(frames))
	for _, frame := range frames {
		path := frame.File
		if path != "" && !filepath.IsAbs(path) && frame.Dir != "" {
			path = filepath.Join(frame.Dir, path)
		}
		path = diagnostic.NormalizePath(path, roots)
		runtime := isRuntimeFrame(frame.Object, path)
		converted = append(converted, diagnostic.Frame{
			Function: strings.TrimSpace(frame.Function),
			Path:     path,
			Object:   strings.TrimSpace(frame.Object),
			Line:     frame.Line,
			Runtime:  runtime,
		})
	}
	return converted
}

// isRuntimeFrame reports whether a frame belongs to Valgrind's own runtime.
// It only uses paths reported by the tool; a user function named malloc is not
// reclassified on name alone.
func isRuntimeFrame(object, path string) bool {
	base := filepath.Base(object)
	if strings.HasPrefix(base, "vgpreload_") {
		return true
	}
	for _, candidate := range []string{object, path} {
		if candidate == "" {
			continue
		}
		slash := filepath.ToSlash(candidate)
		if strings.Contains(slash, "/usr/libexec/valgrind/") ||
			strings.Contains(slash, "/usr/lib/valgrind/") ||
			strings.Contains(slash, "/usr/src/valgrind-") ||
			strings.Contains(slash, "/usr/src/valgrind/") ||
			strings.Contains(slash, "/valgrind/vgpreload_") ||
			strings.HasSuffix(slash, "/vg_replace_malloc.c") {
			return true
		}
	}
	return false
}

func osName() string   { return runtime.GOOS }
func archName() string { return runtime.GOARCH }

// severityForKind is the documented J3 severity policy. It is not a claim by
// Valgrind: reachable blocks are not automatically leaks.
func severityForKind(kind string) diagnostic.Severity {
	switch kind {
	case "Leak_PossiblyLost":
		return diagnostic.SeverityWarning
	case "Leak_StillReachable":
		return diagnostic.SeverityInfo
	case "InvalidRead", "InvalidWrite", "InvalidFree", "MismatchedFree", "Overlap",
		"InvalidJump", "InvalidMemPool", "UninitCondition", "UninitValue",
		"SyscallParam", "ClientCheck", "Leak_DefinitelyLost", "Leak_IndirectlyLost":
		return diagnostic.SeverityError
	default:
		return diagnostic.SeverityWarning
	}
}
