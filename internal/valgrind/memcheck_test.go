package valgrind

import (
	"os"
	"strings"
	"testing"

	"github.com/Morditux/traceanvil-mcp/internal/diagnostic"
)

const fixtureRoot = "/home/dev/project"

func buildFixtureReport(t *testing.T, name string) diagnostic.Report {
	t.Helper()
	result, _ := parseFixture(t, name)
	return BuildReport(BuildInput{
		RunID:       "run_0123456789abcdef0123456789abcdef",
		ToolVersion: "valgrind-3.26.0",
		Roots:       []string{fixtureRoot},
		Options:     DefaultOptions(),
		XML:         result,
	})
}

func TestBuildReportInvalidRead(t *testing.T) {
	report := buildFixtureReport(t, "invalid_read.xml")
	if !report.AnalysisComplete {
		t.Fatalf("AnalysisComplete = false, warnings: %v", report.Warnings)
	}
	if len(report.Diagnostics) != 1 {
		t.Fatalf("len(Diagnostics) = %d, want 1", len(report.Diagnostics))
	}
	d := report.Diagnostics[0]
	if d.Kind != "InvalidRead" || d.Severity != diagnostic.SeverityError {
		t.Errorf("diagnostic = %+v", d)
	}
	if d.Location == nil || d.Location.Function != "main" ||
		d.Location.Path != "build/fixtures/invalid_read.c" || d.Location.Line != 8 {
		t.Errorf("Location = %+v, want main at build/fixtures/invalid_read.c:8", d.Location)
	}
	if d.Occurrences != 1 || d.Evidence.Artifact != RawArtifactName || d.Evidence.RecordID != "0x0" {
		t.Errorf("evidence = %+v", d.Evidence)
	}
	if !strings.HasPrefix(d.Fingerprint, "sha256:") || d.FingerprintAlgorithm != diagnostic.FingerprintAlgorithmV1 {
		t.Errorf("fingerprint = %q algorithm=%q", d.Fingerprint, d.FingerprintAlgorithm)
	}
	if len(d.ContextStacks) != 1 || len(d.ContextStacks[0].Frames) != 2 {
		t.Fatalf("ContextStacks = %+v, want the allocation stack", d.ContextStacks)
	}
	if !d.ContextStacks[0].Frames[0].Runtime {
		t.Error("vgpreload malloc frame was not flagged as runtime")
	}
	if d.ContextStacks[0].Frames[1].Runtime {
		t.Error("user main frame was flagged as runtime")
	}
	if report.Summary.Total != 1 || report.Summary.DiagnosticRecords != 1 {
		t.Errorf("Summary = %+v", report.Summary)
	}
	if report.Summary.ByKind["InvalidRead"] != 1 {
		t.Errorf("ByKind = %+v", report.Summary.ByKind)
	}
}

func TestBuildReportRepeatedOccurrences(t *testing.T) {
	report := buildFixtureReport(t, "repeat.xml")
	if report.Summary.Total != 3 || report.Summary.DiagnosticRecords != 1 {
		t.Fatalf("Summary = %+v, want total 3 and one record", report.Summary)
	}
	if report.Diagnostics[0].Occurrences != 3 {
		t.Errorf("Occurrences = %d, want 3", report.Diagnostics[0].Occurrences)
	}
}

func TestBuildReportLeaks(t *testing.T) {
	report := buildFixtureReport(t, "leak.xml")
	if len(report.Diagnostics) != 2 {
		t.Fatalf("len(Diagnostics) = %d, want 2", len(report.Diagnostics))
	}
	definite := report.Diagnostics[0]
	if definite.Kind != "Leak_DefinitelyLost" || definite.Severity != diagnostic.SeverityError {
		t.Errorf("definitely-lost = %+v", definite)
	}
	if definite.LeakedBytes != 8 || definite.LeakedBlocks != 1 {
		t.Errorf("leak counters = %d bytes / %d blocks", definite.LeakedBytes, definite.LeakedBlocks)
	}
	reachable := report.Diagnostics[1]
	if reachable.Kind != "Leak_StillReachable" || reachable.Severity != diagnostic.SeverityInfo {
		t.Errorf("still-reachable = %+v", reachable)
	}
	if report.Summary.LeakedBytes != 24 || report.Summary.LeakedBlocks != 2 {
		t.Errorf("Summary leaks = %+v", report.Summary)
	}
}

func TestBuildReportNoErrors(t *testing.T) {
	report := buildFixtureReport(t, "no_errors.xml")
	if !report.AnalysisComplete {
		t.Fatalf("AnalysisComplete = false, warnings: %v", report.Warnings)
	}
	if report.Summary.Total != 0 || len(report.Diagnostics) != 0 {
		t.Errorf("report = %+v, want zero diagnostics", report.Summary)
	}
}

func TestBuildReportSuppressedIsNotClean(t *testing.T) {
	report := buildFixtureReport(t, "suppressed.xml")
	if !report.AnalysisComplete {
		t.Fatalf("AnalysisComplete = false, warnings: %v", report.Warnings)
	}
	if report.Summary.Suppressed != 1 || len(report.Suppressions) != 1 {
		t.Fatalf("suppressions = %+v summary=%+v", report.Suppressions, report.Summary)
	}
	if report.Diagnostics == nil || len(report.Diagnostics) != 0 {
		t.Errorf("Diagnostics = %+v, want an empty array", report.Diagnostics)
	}
	found := false
	for _, warning := range report.Warnings {
		if strings.Contains(warning, "not evidence of absence") {
			found = true
		}
	}
	if !found {
		t.Errorf("Warnings = %v, want a suppression warning", report.Warnings)
	}
}

func TestBuildReportTruncated(t *testing.T) {
	report := buildFixtureReport(t, "truncated.xml")
	if report.AnalysisComplete {
		t.Error("AnalysisComplete = true for a truncated document")
	}
	if len(report.Diagnostics) == 0 {
		t.Error("partial diagnostics were discarded")
	}
	if report.Summary.Total == 0 {
		t.Error("Summary.Total = 0 despite a parsed diagnostic")
	}
}

func TestBuildReportMalformed(t *testing.T) {
	report := buildFixtureReport(t, "malformed.xml")
	if report.AnalysisComplete {
		t.Error("AnalysisComplete = true for malformed XML")
	}
	if len(report.Warnings) == 0 {
		t.Error("Warnings is empty, want a parse warning")
	}
}

func TestBuildReportUnknownFields(t *testing.T) {
	report := buildFixtureReport(t, "unknown_fields.xml")
	if !report.AnalysisComplete {
		t.Fatalf("AnalysisComplete = false, warnings: %v", report.Warnings)
	}
	if len(report.Diagnostics) != 1 {
		t.Errorf("len(Diagnostics) = %d, want 1", len(report.Diagnostics))
	}
}

func TestBuildReportSummaryMismatchIsIncomplete(t *testing.T) {
	result, err := parseFixture(t, "invalid_read.xml")
	if err != nil {
		t.Fatal(err)
	}
	result.ErrorSummary.Errors = 2
	report := BuildReport(BuildInput{
		RunID:       "run_0123456789abcdef0123456789abcdef",
		ToolVersion: "valgrind-3.26.0",
		Roots:       []string{fixtureRoot},
		Options:     DefaultOptions(),
		XML:         result,
	})
	if report.AnalysisComplete {
		t.Error("AnalysisComplete = true despite a summary mismatch")
	}
	if len(report.Warnings) == 0 {
		t.Error("Warnings is empty, want a mismatch warning")
	}
}

func TestBuildReportMissingSummaryIsIncomplete(t *testing.T) {
	result, err := parseFixture(t, "invalid_read.xml")
	if err != nil {
		t.Fatal(err)
	}
	result.ErrorSummary = nil
	report := BuildReport(BuildInput{
		RunID:       "run_0123456789abcdef0123456789abcdef",
		ToolVersion: "valgrind-3.26.0",
		Roots:       []string{fixtureRoot},
		Options:     DefaultOptions(),
		XML:         result,
	})
	if report.AnalysisComplete {
		t.Error("AnalysisComplete = true without an error_summary")
	}
}

func TestBuildReportFingerprintIgnoresRuntimeData(t *testing.T) {
	first := buildFixtureReport(t, "invalid_read.xml")
	second := buildFixtureReport(t, "invalid_read.xml")
	if first.Diagnostics[0].Fingerprint != second.Diagnostics[0].Fingerprint {
		t.Error("fingerprint is not stable across identical XML documents")
	}

	result, err := parseFixture(t, "invalid_read.xml")
	if err != nil {
		t.Fatal(err)
	}
	result.PID = 999
	result.Errors[0].Stack[0].Object = "/tmp/other-build/invalid_read"
	secondReport := BuildReport(BuildInput{
		RunID:       "run_ffffffffffffffffffffffffffffffff",
		ToolVersion: "valgrind-3.26.0",
		Roots:       []string{fixtureRoot},
		Options:     DefaultOptions(),
		XML:         result,
	})
	if first.Diagnostics[0].Fingerprint != secondReport.Diagnostics[0].Fingerprint {
		t.Error("fingerprint changed after PID and temporary object path changes")
	}
}

func TestBuildReportValidate(t *testing.T) {
	report := buildFixtureReport(t, "invalid_read.xml")
	report.Result = diagnostic.ResultErrorsFound
	if err := report.Validate(); err != nil {
		t.Errorf("Validate() error = %v", err)
	}

	report.Result = diagnostic.ResultNoErrorsObserved
	report.AnalysisComplete = false
	if err := report.Validate(); err == nil {
		t.Error("Validate() accepted no_errors_observed with analysis_complete=false")
	}
}

func TestBuildReportWithoutXML(t *testing.T) {
	report := BuildReport(BuildInput{
		RunID:       "run_0123456789abcdef0123456789abcdef",
		ToolVersion: "valgrind-3.26.0",
		Options:     DefaultOptions(),
	})
	if report.AnalysisComplete {
		t.Error("AnalysisComplete = true without XML")
	}
	if len(report.Warnings) == 0 {
		t.Error("Warnings is empty, want a missing-evidence warning")
	}
	if report.Summary.Total != 0 {
		t.Errorf("Summary.Total = %d, want 0", report.Summary.Total)
	}
}

func TestSeverityPolicy(t *testing.T) {
	cases := map[string]diagnostic.Severity{
		"InvalidRead":          diagnostic.SeverityError,
		"Leak_DefinitelyLost":  diagnostic.SeverityError,
		"Leak_PossiblyLost":    diagnostic.SeverityWarning,
		"Leak_StillReachable":  diagnostic.SeverityInfo,
		"SomethingNewFromTool": diagnostic.SeverityWarning,
	}
	for kind, want := range cases {
		if got := severityForKind(kind); got != want {
			t.Errorf("severityForKind(%q) = %q, want %q", kind, got, want)
		}
	}
}

func TestFixtureFilesAreReadable(t *testing.T) {
	entries, err := os.ReadDir(fixturePath(t, "."))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 5 {
		t.Fatalf("fixture directory has only %d entries", len(entries))
	}
}

func TestBuildReportMissingSymbolsDoesNotInventLocation(t *testing.T) {
	const document = `<valgrindoutput>
<protocolversion>6</protocolversion><protocoltool>memcheck</protocoltool><tool>memcheck</tool>
<error>
  <unique>0x4</unique><kind>InvalidRead</kind><what>Invalid read of size 4</what>
  <stack><frame><ip>0x1234</ip><obj>/ws/build/fixture</obj></frame></stack>
</error>
<status><state>FINISHED</state></status>
<errorcounts><pair><count>1</count><unique>0x4</unique></pair></errorcounts>
<suppcounts></suppcounts>
<error_summary><errors>1</errors><error_contexts>1</error_contexts><suppressed>0</suppressed></error_summary>
</valgrindoutput>`
	result, err := Parse(strings.NewReader(document), ParseOptions{})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	report := BuildReport(BuildInput{
		RunID: "run_0123456789abcdef0123456789abcdef", ToolVersion: "valgrind-3.26.0",
		Roots: []string{"/ws"}, Options: DefaultOptions(), XML: result,
	})
	if len(report.Diagnostics) != 1 {
		t.Fatalf("len(Diagnostics) = %d", len(report.Diagnostics))
	}
	diag := report.Diagnostics[0]
	if diag.Location != nil {
		t.Errorf("Location = %+v, want nil for a frame without symbols", diag.Location)
	}
	if len(diag.Stack) != 1 || diag.Stack[0].Path != "" || diag.Stack[0].Line != 0 {
		t.Errorf("Stack = %+v, want no invented path or line", diag.Stack)
	}
	if diag.Fingerprint == "" {
		t.Error("Fingerprint is empty")
	}
}
