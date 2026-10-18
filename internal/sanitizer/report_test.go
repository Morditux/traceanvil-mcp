package sanitizer

import (
	"testing"

	"github.com/Morditux/traceanvil-mcp/internal/diagnostic"
)

func TestBuildReportNeverClaimsCleanFromMissingEvidence(t *testing.T) {
	base := BuildInput{
		Tool: ToolASan, RunID: "run_000000000000000000000000000000aa",
		ParseResult: Result{Issues: []Issue{}, Warnings: []string{}},
		Program:     diagnostic.Program{TargetID: "fixture", ExitCode: 0},
		Artifact:    diagnostic.Artifact{Name: StderrArtifactName, Kind: "raw_log", Bytes: 10},
	}
	clean := BuildReport(base)
	if clean.Result != diagnostic.ResultNoErrorsObserved || !clean.AnalysisComplete {
		t.Fatalf("clean = %+v", clean)
	}
	if len(clean.Warnings) == 0 {
		t.Error("clean run must warn that absence is not proof")
	}

	incomplete := base
	incomplete.Program.ExitCode = 1
	incomplete.Program.Signal = 11
	incomplete.ParseResult.Incomplete = true
	incomplete.ParseResult.Warnings = []string{"unknown output"}
	report := BuildReport(incomplete)
	if report.Result != diagnostic.ResultIncomplete || report.AnalysisComplete {
		t.Fatalf("incomplete = %+v", report)
	}
}

func TestBuildReportParsedIssue(t *testing.T) {
	result := Result{
		Issues: []Issue{{
			Kind: "AddressSanitizer/heap-buffer-overflow", Severity: diagnostic.SeverityError,
			Message: "WRITE of size 4", Stack: []diagnostic.Frame{{Function: "main", Path: "/ws/asan.c", Line: 2}},
		}},
		Recognized: true,
	}
	report := BuildReport(BuildInput{
		Tool: ToolASan, RunID: "run_000000000000000000000000000000bb", Roots: []string{"/ws"},
		Issues: result.Issues, ParseResult: result, Program: diagnostic.Program{TargetID: "fixture", ExitCode: 1},
		Artifact: diagnostic.Artifact{Name: StderrArtifactName, Kind: "raw_log", Bytes: 100},
	})
	if report.Result != diagnostic.ResultErrorsFound || !report.AnalysisComplete {
		t.Fatalf("report = %+v", report)
	}
	if len(report.Diagnostics) != 1 || report.Diagnostics[0].Location.Path != "asan.c" {
		t.Fatalf("diagnostics = %+v", report.Diagnostics)
	}
	if report.Artifacts[0].Name != StderrArtifactName {
		t.Errorf("artifact = %+v", report.Artifacts)
	}
}

func TestTSanPreviousAccessAffectsFingerprint(t *testing.T) {
	base := Issue{
		Kind: "DataRace", Severity: diagnostic.SeverityError, Message: "race",
		Stack:         []diagnostic.Frame{{Function: "reader", Path: "/ws/race.c", Line: 3}},
		PreviousStack: []diagnostic.Frame{{Function: "writer", Path: "/ws/race.c", Line: 4}},
	}
	first := BuildReport(BuildInput{
		Tool: ToolTSan, RunID: "run_00000000000000000000000000000031",
		Roots: []string{"/ws"}, Issues: []Issue{base},
		ParseResult: Result{Issues: []Issue{base}, Recognized: true},
		Program:     diagnostic.Program{TargetID: "fixture"}, Artifact: diagnostic.Artifact{Name: StderrArtifactName, Kind: "raw_log"},
	})
	if len(first.Diagnostics) != 1 || len(first.Diagnostics[0].ContextStacks) != 1 {
		t.Fatalf("report = %+v", first)
	}
	changed := base
	changed.PreviousStack = []diagnostic.Frame{{Function: "other_writer", Path: "/ws/race.c", Line: 5}}
	second := BuildReport(BuildInput{
		Tool: ToolTSan, RunID: "run_00000000000000000000000000000032",
		Roots: []string{"/ws"}, Issues: []Issue{changed},
		ParseResult: Result{Issues: []Issue{changed}, Recognized: true},
		Program:     diagnostic.Program{TargetID: "fixture"}, Artifact: diagnostic.Artifact{Name: StderrArtifactName, Kind: "raw_log"},
	})
	if first.Diagnostics[0].Fingerprint == second.Diagnostics[0].Fingerprint {
		t.Error("previous-access stack is missing from the fingerprint")
	}
}

func TestObjectOnlyFramesHaveDistinctFingerprints(t *testing.T) {
	build := func(object string) diagnostic.Report {
		issue := Issue{
			Kind: "AddressSanitizer/SEGV", Severity: diagnostic.SeverityError, Message: "segfault",
			Stack: []diagnostic.Frame{{Object: object}},
		}
		return BuildReport(BuildInput{
			Tool: ToolASan, RunID: "run_00000000000000000000000000000041",
			Issues: []Issue{issue}, ParseResult: Result{Issues: []Issue{issue}, Recognized: true},
			Program:  diagnostic.Program{TargetID: "fixture"},
			Artifact: diagnostic.Artifact{Name: SanitizerArtifactName, Kind: "raw_log"},
		})
	}
	first := build("/p/a.out+0x1234")
	second := build("/p/a.out+0x5678")
	if first.Diagnostics[0].Fingerprint == second.Diagnostics[0].Fingerprint {
		t.Error("object-only frames with different offsets share a fingerprint")
	}
}

func TestPreviousStackStillContributesWithDeepCurrentStack(t *testing.T) {
	current := make([]diagnostic.Frame, 20)
	for i := range current {
		current[i] = diagnostic.Frame{Function: "current", Path: "/ws/race.c", Line: i + 1}
	}
	build := func(previous string) diagnostic.Report {
		issue := Issue{
			Kind: "DataRace", Severity: diagnostic.SeverityError, Message: "race",
			Stack: current, PreviousStack: []diagnostic.Frame{{Function: previous, Path: "/ws/race.c", Line: 30}},
		}
		return BuildReport(BuildInput{
			Tool: ToolTSan, RunID: "run_00000000000000000000000000000042",
			Roots: []string{"/ws"}, Issues: []Issue{issue},
			ParseResult: Result{Issues: []Issue{issue}, Recognized: true},
			Program:     diagnostic.Program{TargetID: "fixture"},
			Artifact:    diagnostic.Artifact{Name: SanitizerArtifactName, Kind: "raw_log"},
		})
	}
	if build("writer_a").Diagnostics[0].Fingerprint == build("writer_b").Diagnostics[0].Fingerprint {
		t.Error("previous stack was hidden by a deep current stack")
	}
}
