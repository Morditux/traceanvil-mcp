package analysis

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Morditux/traceanvil-mcp/internal/diagnostic"
	"github.com/Morditux/traceanvil-mcp/internal/valgrind"
)

func diffAnalysisDiagnostic(i int, fingerprint string) diagnostic.Diagnostic {
	return diagnostic.Diagnostic{
		ID:                   fmt.Sprintf("diag_%d", i),
		Fingerprint:          "sha256:" + strings.Repeat(fingerprint, 64/len(fingerprint)),
		FingerprintAlgorithm: diagnostic.FingerprintAlgorithmV2,
		Origin:               valgrind.Origin,
		Kind:                 "InvalidRead",
		Severity:             diagnostic.SeverityError,
		Message:              "Invalid read of size 4",
		Location:             &diagnostic.Location{Function: "main", Path: "fixture.c", Line: 8},
		Stack:                []diagnostic.Frame{{Function: "main", Path: "fixture.c", Line: 8}},
		Occurrences:          1,
		Evidence: diagnostic.Evidence{
			Artifact: valgrind.RawArtifactName, RecordID: fmt.Sprintf("0x%d", i), RecordIndex: i,
		},
		Confidence: diagnostic.ConfidenceToolReported,
	}
}

func diffAnalysisReport(runID string, diagnostics []diagnostic.Diagnostic) diagnostic.Report {
	if diagnostics == nil {
		diagnostics = []diagnostic.Diagnostic{}
	}
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
		Options:     map[string]any{"leak_check": "full"},
		Summary:     diagnostic.BuildSummary(diagnostics, nil, false),
		Diagnostics: diagnostics,
		Artifacts:   []diagnostic.Artifact{},
		Warnings:    []string{},
	}
}

func TestDiffServiceClassifications(t *testing.T) {
	store := newTestStore(t)
	service := newTestService(t, store, newFakeBackend(), nil)
	ctx := context.Background()

	baseID := writeReportRun(t, store, diffAnalysisReport("", []diagnostic.Diagnostic{
		diffAnalysisDiagnostic(1, "aa"),
		diffAnalysisDiagnostic(2, "bb"),
	}))
	targetID := writeReportRun(t, store, diffAnalysisReport("", []diagnostic.Diagnostic{
		diffAnalysisDiagnostic(1, "aa"),
		diffAnalysisDiagnostic(3, "cc"),
	}))

	result, err := service.Diff(ctx, DiffRequest{BaseRunID: baseID, TargetRunID: targetID})
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}
	if result.SchemaVersion != DiffSchemaVersion {
		t.Errorf("schema_version = %d", result.SchemaVersion)
	}
	if result.Summary.Persistent != 1 || result.Summary.Resolved != 1 || result.Summary.New != 1 || result.Summary.Uncertain != 0 {
		t.Errorf("summary = %+v", result.Summary)
	}
	if result.Page.Matched != 3 || result.Page.Returned != 3 || result.Page.HasMore {
		t.Errorf("page = %+v", result.Page)
	}
	if len(result.Compatibility.Changes) != 0 || len(result.Compatibility.Uncertainties) != 0 {
		t.Errorf("compatibility = %+v", result.Compatibility)
	}
}

func TestDiffServicePaginationAndCursor(t *testing.T) {
	store := newTestStore(t)
	service := newTestService(t, store, newFakeBackend(), func(opts *Options) {
		opts.MaxPageItems = 2
	})
	ctx := context.Background()

	var baseDiagnostics []diagnostic.Diagnostic
	for i := 0; i < 6; i++ {
		baseDiagnostics = append(baseDiagnostics, diffAnalysisDiagnostic(i, fmt.Sprintf("%02x", i+1)))
	}
	baseID := writeReportRun(t, store, diffAnalysisReport("", baseDiagnostics))
	target := diffAnalysisReport("", nil)
	target.Result = diagnostic.ResultNoErrorsObserved
	target.Summary = diagnostic.BuildSummary(nil, nil, false)
	targetID := writeReportRun(t, store, target)

	seen := map[string]bool{}
	cursor := ""
	for pageNo := 0; pageNo < 4; pageNo++ {
		page, err := service.Diff(ctx, DiffRequest{BaseRunID: baseID, TargetRunID: targetID, Cursor: cursor})
		if err != nil {
			t.Fatalf("Diff() page %d error = %v", pageNo, err)
		}
		if page.Page.Returned > 2 || page.Page.Matched != 6 || page.Summary.Resolved != 6 {
			t.Fatalf("page %d = %+v", pageNo, page.Page)
		}
		for _, item := range page.Items {
			if item.Base == nil || seen[item.Base.ID] {
				t.Fatalf("duplicate or missing item %+v", item)
			}
			seen[item.Base.ID] = true
		}
		if !page.Page.HasMore {
			if page.Page.NextCursor != "" || len(seen) != 6 {
				t.Fatalf("final page = %+v, seen=%d", page.Page, len(seen))
			}
			return
		}
		cursor = page.Page.NextCursor
	}
	t.Fatal("pagination did not converge")
}

func TestDiffServiceIncompatibleMetadata(t *testing.T) {
	store := newTestStore(t)
	service := newTestService(t, store, newFakeBackend(), nil)
	baseID := writeReportRun(t, store, diffAnalysisReport("", []diagnostic.Diagnostic{diffAnalysisDiagnostic(1, "aa")}))
	target := diffAnalysisReport("", []diagnostic.Diagnostic{diffAnalysisDiagnostic(2, "aa")})
	target.Tool.Name = "helgrind"
	targetID := writeReportRun(t, store, target)
	_, err := service.Diff(context.Background(), DiffRequest{BaseRunID: baseID, TargetRunID: targetID})
	if got := CodeOf(err); got != CodeIncompatibleRuns {
		t.Fatalf("Diff() error = %v, want %s", err, CodeIncompatibleRuns)
	}
}

func TestDiffServiceConfigurationChangeIsUncertain(t *testing.T) {
	store := newTestStore(t)
	service := newTestService(t, store, newFakeBackend(), nil)
	baseID := writeReportRun(t, store, diffAnalysisReport("", []diagnostic.Diagnostic{diffAnalysisDiagnostic(1, "aa")}))
	target := diffAnalysisReport("", []diagnostic.Diagnostic{diffAnalysisDiagnostic(1, "aa")})
	target.Options = map[string]any{"leak_check": "summary"}
	targetID := writeReportRun(t, store, target)
	result, err := service.Diff(context.Background(), DiffRequest{BaseRunID: baseID, TargetRunID: targetID})
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}
	if len(result.Compatibility.Changes) == 0 || result.Summary.Uncertain != 1 || result.Summary.Persistent != 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestDiffServiceRejectsSameAndMissingRuns(t *testing.T) {
	store := newTestStore(t)
	service := newTestService(t, store, newFakeBackend(), nil)
	if _, err := service.Diff(context.Background(), DiffRequest{
		BaseRunID: testRunID, TargetRunID: testRunID,
	}); CodeOf(err) != CodeInvalidArgument {
		t.Fatalf("same run error = %v", err)
	}
	if _, err := service.Diff(context.Background(), DiffRequest{
		BaseRunID: testRunID, TargetRunID: "run_00000000000000000000000000000002",
	}); CodeOf(err) != CodeRunNotFound {
		t.Fatalf("missing run error = %v", err)
	}
}

func TestDiffServiceByteBudgetAndOversizedItem(t *testing.T) {
	store := newTestStore(t)
	service := newTestService(t, store, newFakeBackend(), func(opts *Options) {
		opts.MaxPageBytes = 1
	})
	baseID := writeReportRun(t, store, diffAnalysisReport("", []diagnostic.Diagnostic{diffAnalysisDiagnostic(1, "aa")}))
	target := diffAnalysisReport("", nil)
	target.Result = diagnostic.ResultNoErrorsObserved
	target.Summary = diagnostic.BuildSummary(nil, nil, false)
	targetID := writeReportRun(t, store, target)
	if _, err := service.Diff(context.Background(), DiffRequest{BaseRunID: baseID, TargetRunID: targetID}); CodeOf(err) != CodeDiffTooLarge {
		t.Fatalf("Diff() error = %v, want %s", err, CodeDiffTooLarge)
	}
}

func TestDiffServiceCursorIsBoundToBothRuns(t *testing.T) {
	store := newTestStore(t)
	service := newTestService(t, store, newFakeBackend(), func(opts *Options) {
		opts.MaxPageItems = 1
	})
	baseDiagnostics := []diagnostic.Diagnostic{
		diffAnalysisDiagnostic(1, "aa"),
		diffAnalysisDiagnostic(2, "bb"),
	}
	baseID := writeReportRun(t, store, diffAnalysisReport("", baseDiagnostics))
	firstTarget := diffAnalysisReport("", []diagnostic.Diagnostic{diffAnalysisDiagnostic(3, "cc")})
	firstTargetID := writeReportRun(t, store, firstTarget)
	page, err := service.Diff(context.Background(), DiffRequest{BaseRunID: baseID, TargetRunID: firstTargetID})
	if err != nil {
		t.Fatal(err)
	}
	if !page.Page.HasMore || page.Page.NextCursor == "" {
		t.Fatalf("page = %+v", page.Page)
	}
	secondTarget := diffAnalysisReport("", []diagnostic.Diagnostic{diffAnalysisDiagnostic(4, "dd")})
	secondTargetID := writeReportRun(t, store, secondTarget)
	if _, err := service.Diff(context.Background(), DiffRequest{
		BaseRunID: baseID, TargetRunID: secondTargetID, Cursor: page.Page.NextCursor,
	}); CodeOf(err) != CodeInvalidCursor {
		t.Fatalf("cursor reuse error = %v, want %s", err, CodeInvalidCursor)
	}
}

func TestLoadFullReportMarksUnreadableRecordsTruncated(t *testing.T) {
	store := newTestStore(t)
	service := newTestService(t, store, newFakeBackend(), func(opts *Options) {
		opts.MaxPageBytes = 256
		opts.MaxDiagnosticBytes = 256
	})
	item := diffAnalysisDiagnostic(1, "aa")
	item.Message = strings.Repeat("x", 1024)
	runID := writeReportRun(t, store, diffAnalysisReport("", []diagnostic.Diagnostic{item}))
	report, err := service.loadFullReport(context.Background(), runID)
	if err != nil {
		t.Fatalf("loadFullReport() error = %v", err)
	}
	if !report.Summary.Truncated {
		t.Fatalf("Summary.Truncated = false, report = %+v", report.Summary)
	}
	if len(report.Diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v, want the unreadable record omitted", report.Diagnostics)
	}
	if len(report.Warnings) == 0 || !strings.Contains(strings.Join(report.Warnings, " "), "unreadable") {
		t.Errorf("warnings = %v", report.Warnings)
	}
}

func TestDiffServiceUnreadableRecordBecomesUncertain(t *testing.T) {
	store := newTestStore(t)
	service := newTestService(t, store, newFakeBackend(), func(opts *Options) {
		opts.MaxPageBytes = 4096
		opts.MaxDiagnosticBytes = 4096
	})
	baseID := writeReportRun(t, store, diffAnalysisReport("", []diagnostic.Diagnostic{diffAnalysisDiagnostic(1, "aa")}))
	oversized := diffAnalysisDiagnostic(1, "aa")
	oversized.Message = strings.Repeat("x", 10000)
	targetID := writeReportRun(t, store, diffAnalysisReport("", []diagnostic.Diagnostic{oversized}))
	result, err := service.Diff(context.Background(), DiffRequest{BaseRunID: baseID, TargetRunID: targetID})
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}
	if result.Summary.Uncertain != 1 || result.Summary.Resolved != 0 {
		t.Fatalf("summary = %+v, want one uncertain item and no resolved item", result.Summary)
	}
	if len(result.Compatibility.Uncertainties) == 0 {
		t.Fatal("unreadable target record was not reported as an uncertainty")
	}
}

func TestDiffServiceResponseByteBoundIncludesEnvelope(t *testing.T) {
	store := newTestStore(t)
	const maxBytes = 4096
	service := newTestService(t, store, newFakeBackend(), func(opts *Options) {
		opts.MaxPageBytes = maxBytes
		opts.MaxPageItems = 50
	})
	var baseDiagnostics []diagnostic.Diagnostic
	for i := 0; i < 20; i++ {
		item := diffAnalysisDiagnostic(i, fmt.Sprintf("%02x", i+1))
		item.Message = strings.Repeat("m", 300)
		baseDiagnostics = append(baseDiagnostics, item)
	}
	baseID := writeReportRun(t, store, diffAnalysisReport("", baseDiagnostics))
	target := diffAnalysisReport("", nil)
	target.Result = diagnostic.ResultNoErrorsObserved
	target.Summary = diagnostic.BuildSummary(nil, nil, false)
	targetID := writeReportRun(t, store, target)
	result, err := service.Diff(context.Background(), DiffRequest{BaseRunID: baseID, TargetRunID: targetID})
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > maxBytes {
		t.Fatalf("encoded diff = %d bytes, want <= %d", len(encoded), maxBytes)
	}
	if !result.Page.HasMore || result.Page.NextCursor == "" {
		t.Fatalf("page = %+v, want a continuation", result.Page)
	}
}
