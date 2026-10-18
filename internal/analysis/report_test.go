package analysis

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Morditux/traceanvil-mcp/internal/diagnostic"
	"github.com/Morditux/traceanvil-mcp/internal/jobs"
	"github.com/Morditux/traceanvil-mcp/internal/valgrind"
)

func minimalDiagnostic(i int) diagnostic.Diagnostic {
	return diagnostic.Diagnostic{
		ID:                   fmt.Sprintf("diag_%d", i),
		Fingerprint:          "sha256:" + strings.Repeat("ab", 32),
		FingerprintAlgorithm: diagnostic.FingerprintAlgorithmV1,
		Origin:               valgrind.Origin,
		Kind:                 "InvalidRead",
		Severity:             diagnostic.SeverityError,
		Message:              "Invalid read of size 4",
		Occurrences:          1,
		Evidence: diagnostic.Evidence{
			Artifact:    valgrind.RawArtifactName,
			RecordID:    "0x0",
			RecordIndex: i,
		},
		Confidence: diagnostic.ConfidenceToolReported,
	}
}

func TestReportPaginationBoundedWith20kDiagnostics(t *testing.T) {
	store := newTestStore(t)
	diagnostics := make([]diagnostic.Diagnostic, 20000)
	for i := range diagnostics {
		diagnostics[i] = minimalDiagnostic(i)
		if i%2 == 0 {
			diagnostics[i].Kind = "InvalidWrite"
		}
	}
	report := sampleReport("", diagnostics)
	runID := writeReportRun(t, store, report)

	service := newTestService(t, store, newFakeBackend(), func(opts *Options) {
		opts.MaxResponseItems = 1000
		opts.MaxPageItems = 500
		opts.MaxPageBytes = 1 << 20
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	seen := make(map[string]struct{}, len(diagnostics))
	cursor := ""
	total := 0
	pages := 0
	for {
		page, err := service.Report(ctx, ReportRequest{RunID: runID, Cursor: cursor, Limit: 500})
		if err != nil {
			t.Fatalf("Report() page %d error = %v", pages, err)
		}
		encoded, err := json.Marshal(page)
		if err != nil {
			t.Fatalf("marshal page: %v", err)
		}
		if len(encoded) > 2<<20 {
			t.Fatalf("page %d is %d bytes, want a bounded response", pages, len(encoded))
		}
		if page.Page.Matched != 20000 {
			t.Fatalf("page %d matched = %d, want 20000", pages, page.Page.Matched)
		}
		if page.Page.Returned > 500 {
			t.Fatalf("page %d returned = %d, want <= 500", pages, page.Page.Returned)
		}
		for _, item := range page.Diagnostics {
			if _, duplicate := seen[item.ID]; duplicate {
				t.Fatalf("diagnostic %q returned twice", item.ID)
			}
			seen[item.ID] = struct{}{}
			total++
		}
		pages++
		if !page.Page.HasMore {
			if page.Page.NextCursor != "" {
				t.Errorf("final page has a next_cursor")
			}
			break
		}
		if page.Page.NextCursor == "" {
			t.Fatalf("page %d has_more=true without a next_cursor", pages)
		}
		cursor = page.Page.NextCursor
		if pages > 100 {
			t.Fatal("pagination did not converge")
		}
	}

	if total != 20000 {
		t.Errorf("total diagnostics = %d, want 20000", total)
	}
	if pages != 40 {
		t.Errorf("pages = %d, want 40", pages)
	}
}

func TestReportFilters(t *testing.T) {
	store := newTestStore(t)

	first := sampleDiagnostic(0)
	first.Kind = "InvalidRead"
	first.Severity = diagnostic.SeverityError
	first.Occurrences = 1
	first.Stack = []diagnostic.Frame{{Function: "read_main", Path: "src/read.c", Line: 10}}

	second := sampleDiagnostic(1)
	second.Kind = "InvalidWrite"
	second.Severity = diagnostic.SeverityError
	second.Occurrences = 2
	second.Stack = []diagnostic.Frame{{Function: "write_main", Path: "src/write.c", Line: 20}}

	third := sampleDiagnostic(2)
	third.Kind = "Leak_PossiblyLost"
	third.Severity = diagnostic.SeverityWarning
	third.Occurrences = 3
	third.Stack = []diagnostic.Frame{{Function: "leak_main", Path: "lib/leak.c", Line: 30}}

	fourth := sampleDiagnostic(3)
	fourth.Kind = "Leak_StillReachable"
	fourth.Severity = diagnostic.SeverityInfo
	fourth.Occurrences = 4
	fourth.Stack = nil

	report := sampleReport("", []diagnostic.Diagnostic{first, second, third, fourth})
	runID := writeReportRun(t, store, report)
	service := newTestService(t, store, newFakeBackend(), nil)
	ctx := context.Background()

	tests := []struct {
		name    string
		request ReportRequest
		want    []string
	}{
		{name: "all", request: ReportRequest{}, want: []string{first.ID, second.ID, third.ID, fourth.ID}},
		{name: "kind", request: ReportRequest{Kinds: []string{"InvalidRead", "InvalidWrite"}}, want: []string{first.ID, second.ID}},
		{name: "severity", request: ReportRequest{Severities: []string{"warning"}}, want: []string{third.ID}},
		{name: "path", request: ReportRequest{PathPrefix: "src/"}, want: []string{first.ID, second.ID}},
		{name: "function", request: ReportRequest{Function: "leak_main"}, want: []string{third.ID}},
		{name: "occurrences", request: ReportRequest{MinOccurrences: 3}, want: []string{third.ID, fourth.ID}},
		{name: "combined", request: ReportRequest{
			Kinds:      []string{"InvalidRead", "Leak_PossiblyLost"},
			Severities: []string{"warning"},
		}, want: []string{third.ID}},
		{name: "no match", request: ReportRequest{Kinds: []string{"Overlap"}}, want: []string{}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.request.RunID = runID
			page, err := service.Report(ctx, test.request)
			if err != nil {
				t.Fatalf("Report() error = %v", err)
			}
			if page.Page.Matched != len(test.want) {
				t.Errorf("matched = %d, want %d", page.Page.Matched, len(test.want))
			}
			if page.Page.Returned != len(test.want) {
				t.Errorf("returned = %d, want %d", page.Page.Returned, len(test.want))
			}
			got := make([]string, 0, len(page.Diagnostics))
			for _, item := range page.Diagnostics {
				got = append(got, item.ID)
			}
			if strings.Join(got, ",") != strings.Join(test.want, ",") {
				t.Errorf("diagnostics = %v, want %v", got, test.want)
			}
		})
	}
}

func TestReportCursorScope(t *testing.T) {
	store := newTestStore(t)
	report := sampleReport("", []diagnostic.Diagnostic{
		minimalDiagnostic(0), minimalDiagnostic(1), minimalDiagnostic(2),
	})
	runID := writeReportRun(t, store, report)
	otherReport := sampleReport("", []diagnostic.Diagnostic{minimalDiagnostic(0)})
	otherRunID := writeReportRun(t, store, otherReport)

	service := newTestService(t, store, newFakeBackend(), nil)
	ctx := context.Background()

	first, err := service.Report(ctx, ReportRequest{RunID: runID, Limit: 1})
	if err != nil {
		t.Fatalf("Report() error = %v", err)
	}
	if !first.Page.HasMore || first.Page.NextCursor == "" {
		t.Fatalf("first page = %+v, want a next cursor", first.Page)
	}
	cursor := first.Page.NextCursor

	second, err := service.Report(ctx, ReportRequest{RunID: runID, Cursor: cursor, Limit: 5})
	if err != nil {
		t.Fatalf("Report() with cursor error = %v", err)
	}
	if second.Page.Offset != 1 {
		t.Errorf("offset = %d, want 1", second.Page.Offset)
	}
	if got := len(second.Diagnostics); got != 2 {
		t.Errorf("returned = %d, want 2", got)
	}

	_, err = service.Report(ctx, ReportRequest{
		RunID:  runID,
		Cursor: cursor,
		Kinds:  []string{"InvalidWrite"},
	})
	mustCode(t, err, CodeInvalidCursor)

	_, err = service.Report(ctx, ReportRequest{RunID: otherRunID, Cursor: cursor})
	mustCode(t, err, CodeInvalidCursor)

	_, err = service.Report(ctx, ReportRequest{RunID: runID, Cursor: "not-base64!!"})
	mustCode(t, err, CodeInvalidCursor)

	_, err = service.Report(ctx, ReportRequest{RunID: runID, Cursor: strings.Repeat("a", maxCursorBytes+1)})
	mustCode(t, err, CodeInvalidCursor)
}

func TestReportErrors(t *testing.T) {
	store := newTestStore(t)
	service := newTestService(t, store, newFakeBackend(), func(opts *Options) {
		opts.MaxReportBytes = 4 << 20
	})
	ctx := context.Background()

	_, err := service.Report(ctx, ReportRequest{RunID: "not-a-run"})
	mustCode(t, err, CodeInvalidArgument)

	_, err = service.Report(ctx, ReportRequest{RunID: "run_ffffffffffffffffffffffffffffffff"})
	mustCode(t, err, CodeRunNotFound)

	emptyRun := createRunDir(t, store)
	_, err = service.Report(ctx, ReportRequest{RunID: emptyRun.ID()})
	mustCode(t, err, CodeReportUnavailable)

	truncated := createRunDir(t, store)
	if err := truncated.WriteFileAtomic(valgrind.AnalysisArtifactName, []byte(`{"schema_version":1,"run_id":"`+truncated.ID()+`"`)); err != nil {
		t.Fatal(err)
	}
	_, err = service.Report(ctx, ReportRequest{RunID: truncated.ID()})
	mustCode(t, err, CodeReportInvalid)

	wrongSchema := sampleReport("", []diagnostic.Diagnostic{minimalDiagnostic(0)})
	wrongSchema.SchemaVersion = 2
	wrongSchemaRun := writeReportRun(t, store, wrongSchema)
	_, err = service.Report(ctx, ReportRequest{RunID: wrongSchemaRun})
	mustCode(t, err, CodeReportInvalid)

	mismatch := sampleReport("run_ffffffffffffffffffffffffffffffff", []diagnostic.Diagnostic{minimalDiagnostic(0)})
	mismatchRun := writeReportRun(t, store, mismatch)
	_, err = service.Report(ctx, ReportRequest{RunID: mismatchRun})
	mustCode(t, err, CodeReportInvalid)

	incompleteNoErrors := sampleReport("", nil)
	incompleteNoErrors.Result = diagnostic.ResultNoErrorsObserved
	incompleteNoErrors.AnalysisComplete = false
	incompleteRun := writeReportRun(t, store, incompleteNoErrors)
	_, err = service.Report(ctx, ReportRequest{RunID: incompleteRun})
	mustCode(t, err, CodeReportInvalid)

	errorsZero := sampleReport("", nil)
	errorsZero.Result = diagnostic.ResultErrorsFound
	errorsZero.Summary.Total = 0
	zeroRun := writeReportRun(t, store, errorsZero)
	_, err = service.Report(ctx, ReportRequest{RunID: zeroRun})
	mustCode(t, err, CodeReportInvalid)

	missingEvidence := sampleReport("", []diagnostic.Diagnostic{minimalDiagnostic(0)})
	missingEvidence.Diagnostics[0].Evidence.Artifact = ""
	missingEvidenceRun := writeReportRun(t, store, missingEvidence)
	_, err = service.Report(ctx, ReportRequest{RunID: missingEvidenceRun})
	mustCode(t, err, CodeReportInvalid)

	wrongTotal := sampleReport("", []diagnostic.Diagnostic{minimalDiagnostic(0)})
	wrongTotal.Summary.Total = 5
	wrongTotalRun := writeReportRun(t, store, wrongTotal)
	_, err = service.Report(ctx, ReportRequest{RunID: wrongTotalRun})
	mustCode(t, err, CodeReportInvalid)

	wrongRecords := sampleReport("", []diagnostic.Diagnostic{minimalDiagnostic(0)})
	wrongRecords.Summary.DiagnosticRecords = 5
	wrongRecordsRun := writeReportRun(t, store, wrongRecords)
	_, err = service.Report(ctx, ReportRequest{RunID: wrongRecordsRun})
	mustCode(t, err, CodeReportInvalid)

	hugeOccurrences := sampleReport("", []diagnostic.Diagnostic{minimalDiagnostic(0)})
	hugeOccurrences.Diagnostics[0].Occurrences = maxReportOccurrences + 1
	hugeOccurrences.Summary = diagnostic.BuildSummary(hugeOccurrences.Diagnostics, nil, false)
	hugeOccurrencesRun := writeReportRun(t, store, hugeOccurrences)
	_, err = service.Report(ctx, ReportRequest{RunID: hugeOccurrencesRun})
	mustCode(t, err, CodeReportInvalid)

	duplicate := sampleReport("", []diagnostic.Diagnostic{minimalDiagnostic(0), minimalDiagnostic(0)})
	duplicate.Summary = diagnostic.BuildSummary(duplicate.Diagnostics, nil, false)
	duplicateRun := writeReportRun(t, store, duplicate)
	_, err = service.Report(ctx, ReportRequest{RunID: duplicateRun})
	mustCode(t, err, CodeReportInvalid)

	noDiagnostics := createRunDir(t, store)
	headerOnly := fmt.Sprintf(`{"schema_version":1,"run_id":%q,"tool":{"name":"valgrind"},"result":"no_errors_observed","analysis_complete":true,"summary":{"total":0,"diagnostic_records":0,"by_kind":{},"by_severity":{},"suppressed":0,"truncated":false},"diagnostics":[],"artifacts":[],"warnings":[]}`, noDiagnostics.ID())
	headerWithoutArray := fmt.Sprintf(`{"schema_version":1,"run_id":%q,"tool":{"name":"valgrind"},"result":"no_errors_observed","analysis_complete":true,"summary":{"total":0,"diagnostic_records":0,"by_kind":{},"by_severity":{},"suppressed":0,"truncated":false},"artifacts":[],"warnings":[]}`, noDiagnostics.ID())
	if err := noDiagnostics.WriteFileAtomic(valgrind.AnalysisArtifactName, []byte(headerOnly)); err != nil {
		t.Fatal(err)
	}
	valid, err := service.Report(ctx, ReportRequest{RunID: noDiagnostics.ID()})
	if err != nil {
		t.Fatalf("Report() on a valid zero-diagnostic report error = %v", err)
	}
	if valid.Result != diagnostic.ResultNoErrorsObserved || !valid.AnalysisComplete {
		t.Errorf("zero-diagnostic report = %+v", valid)
	}
	if valid.Summary.ByKind == nil || valid.Summary.BySeverity == nil {
		t.Error("summary maps are nil, want empty objects")
	}

	missingArray := createRunDir(t, store)
	if err := missingArray.WriteFileAtomic(valgrind.AnalysisArtifactName,
		[]byte(strings.Replace(headerWithoutArray, noDiagnostics.ID(), missingArray.ID(), 1))); err != nil {
		t.Fatal(err)
	}
	_, err = service.Report(ctx, ReportRequest{RunID: missingArray.ID()})
	mustCode(t, err, CodeReportInvalid)

	large := sampleReport("", []diagnostic.Diagnostic{minimalDiagnostic(0)})
	large.Warnings = []string{strings.Repeat("w", maxHeaderFieldBytes+1)}
	largeRun := writeReportRun(t, store, large)
	_, err = service.Report(ctx, ReportRequest{RunID: largeRun})
	mustCode(t, err, CodeReportInvalid)

	smallService := newTestService(t, store, newFakeBackend(), func(opts *Options) {
		opts.MaxReportBytes = 512
	})
	_, err = smallService.Report(ctx, ReportRequest{RunID: mismatchRun})
	mustCode(t, err, CodeReportTooLarge)
}

func TestReportWarningsAreBounded(t *testing.T) {
	store := newTestStore(t)
	report := sampleReport("", []diagnostic.Diagnostic{minimalDiagnostic(0)})
	for i := 0; i < maxHeaderWarnings+10; i++ {
		report.Warnings = append(report.Warnings, fmt.Sprintf("warning %d", i))
	}
	runID := writeReportRun(t, store, report)
	service := newTestService(t, store, newFakeBackend(), nil)

	page, err := service.Report(context.Background(), ReportRequest{RunID: runID})
	if err != nil {
		t.Fatalf("Report() error = %v", err)
	}
	if len(page.Warnings) > maxHeaderWarnings+2 {
		t.Errorf("warnings = %d, want at most %d", len(page.Warnings), maxHeaderWarnings+2)
	}
	found := false
	for _, warning := range page.Warnings {
		if strings.Contains(warning, "truncated") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want a truncation warning", page.Warnings)
	}
}

func TestReportOversizedDiagnostics(t *testing.T) {
	store := newTestStore(t)
	large := minimalDiagnostic(0)
	large.Message = strings.Repeat("x", 200_000)
	small := minimalDiagnostic(1)
	report := sampleReport("", []diagnostic.Diagnostic{large, small})
	runID := writeReportRun(t, store, report)

	service := newTestService(t, store, newFakeBackend(), func(opts *Options) {
		opts.MaxPageBytes = 32 << 10
		opts.MaxDiagnosticBytes = 4 << 20
	})
	page, err := service.Report(context.Background(), ReportRequest{RunID: runID})
	if err != nil {
		t.Fatalf("Report() error = %v", err)
	}
	if page.Page.OmittedOversized != 1 {
		t.Errorf("omitted_oversized = %d, want 1", page.Page.OmittedOversized)
	}
	if page.Page.Returned != 1 || len(page.Diagnostics) != 1 || page.Diagnostics[0].ID != small.ID {
		t.Errorf("diagnostics = %+v, want only %q", page.Diagnostics, small.ID)
	}
	if page.Page.Matched != 2 || page.Page.Consumed != 2 {
		t.Errorf("matched/consumed = %d/%d, want 2/2", page.Page.Matched, page.Page.Consumed)
	}
	if page.Page.HasMore {
		t.Error("has_more = true, want false")
	}
	warned := false
	for _, warning := range page.Warnings {
		if strings.Contains(warning, "analysis_artifact") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("warnings = %v, want an artifact fallback warning", page.Warnings)
	}

	unreadableStore := newTestStore(t)
	unreadableReport := sampleReport("", []diagnostic.Diagnostic{minimalDiagnostic(0)})
	unreadableRun := writeReportRun(t, unreadableStore, unreadableReport)
	unreadableService := newTestService(t, unreadableStore, newFakeBackend(), func(opts *Options) {
		opts.MaxPageBytes = 256
		opts.MaxDiagnosticBytes = 256
	})
	unreadable, err := unreadableService.Report(context.Background(), ReportRequest{RunID: unreadableRun})
	if err != nil {
		t.Fatalf("Report() error = %v", err)
	}
	if unreadable.Page.UnreadableDiagnostics == 0 {
		t.Error("unreadable_diagnostics = 0, want at least one")
	}
	if !unreadable.Page.ScanIncomplete {
		t.Error("scan_incomplete = false, want true")
	}
}

func TestReportByteBudgetStopsPage(t *testing.T) {
	store := newTestStore(t)
	diagnostics := make([]diagnostic.Diagnostic, 10)
	for i := range diagnostics {
		diagnostics[i] = minimalDiagnostic(i)
		diagnostics[i].Message = strings.Repeat("m", 400)
	}
	report := sampleReport("", diagnostics)
	runID := writeReportRun(t, store, report)

	service := newTestService(t, store, newFakeBackend(), func(opts *Options) {
		opts.MaxPageBytes = 900
		opts.MaxDiagnosticBytes = 4 << 20
		opts.MaxPageItems = 10
	})
	page, err := service.Report(context.Background(), ReportRequest{RunID: runID, Limit: 10})
	if err != nil {
		t.Fatalf("Report() error = %v", err)
	}
	if !page.Page.ByteBudgetReached {
		t.Error("byte_budget_reached = false, want true")
	}
	if page.Page.Returned == 0 || page.Page.Returned >= len(diagnostics) {
		t.Errorf("returned = %d, want a bounded non-empty page", page.Page.Returned)
	}
	if !page.Page.HasMore || page.Page.NextCursor == "" {
		t.Error("want a continuation cursor after the byte budget")
	}
}

func TestStatusContextCanceled(t *testing.T) {
	store := newTestStore(t)
	service := newTestService(t, store, newFakeBackend(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := service.Status(ctx, StatusRequest{RunID: testRunID})
	mustCode(t, err, CodeCanceled)
}

func TestReadJobManifestRejectsForeignRun(t *testing.T) {
	store := newTestStore(t)
	manifest := jobs.Snapshot{
		SchemaVersion: jobs.SchemaVersion,
		RunID:         "run_ffffffffffffffffffffffffffffffff",
		TargetID:      "fixture",
		State:         jobs.StateCompleted,
	}
	runID := writeManifest(t, store, manifest)
	_, err := readJobManifest(store, runID)
	if err == nil {
		t.Fatal("readJobManifest() error = nil, want a mismatch error")
	}
}

func TestReportStreamingWithoutCache(t *testing.T) {
	store := newTestStore(t)
	diagnostics := make([]diagnostic.Diagnostic, 5)
	for i := range diagnostics {
		diagnostics[i] = minimalDiagnostic(i)
	}
	runID := writeReportRun(t, store, sampleReport("", diagnostics))
	service := newTestService(t, store, newFakeBackend(), func(opts *Options) {
		opts.MaxCacheBytes = -1
	})

	cursor := ""
	total := 0
	for pages := 0; ; pages++ {
		page, err := service.Report(context.Background(), ReportRequest{RunID: runID, Cursor: cursor, Limit: 2})
		if err != nil {
			t.Fatalf("Report() error = %v", err)
		}
		total += len(page.Diagnostics)
		if page.Page.Matched != 5 {
			t.Errorf("matched = %d, want 5", page.Page.Matched)
		}
		if !page.Page.HasMore {
			break
		}
		cursor = page.Page.NextCursor
		if pages > 10 {
			t.Fatal("pagination did not converge")
		}
	}
	if total != 5 {
		t.Errorf("total = %d, want 5", total)
	}
}

func TestReportStreamsAboveCacheEntryBound(t *testing.T) {
	store := newTestStore(t)
	diagnostics := make([]diagnostic.Diagnostic, 20)
	for i := range diagnostics {
		diagnostics[i] = minimalDiagnostic(i)
	}
	runID := writeReportRun(t, store, sampleReport("", diagnostics))
	service := newTestService(t, store, newFakeBackend(), func(opts *Options) {
		opts.MaxCacheEntryBytes = 512
	})

	first, err := service.Report(context.Background(), ReportRequest{RunID: runID, Limit: 10})
	if err != nil {
		t.Fatalf("Report() error = %v", err)
	}
	if first.Page.Matched != 20 || first.Page.Returned != 10 {
		t.Errorf("page = %+v", first.Page)
	}
	second, err := service.Report(context.Background(), ReportRequest{
		RunID:  runID,
		Cursor: first.Page.NextCursor,
		Limit:  10,
	})
	if err != nil {
		t.Fatalf("Report() error = %v", err)
	}
	if second.Page.Returned != 10 || second.Page.HasMore {
		t.Errorf("second page = %+v", second.Page)
	}
}

func TestReportCacheInvalidatesOnReportChange(t *testing.T) {
	store := newTestStore(t)
	report := sampleReport("", []diagnostic.Diagnostic{minimalDiagnostic(0)})
	runID := writeReportRun(t, store, report)
	service := newTestService(t, store, newFakeBackend(), nil)

	first, err := service.Report(context.Background(), ReportRequest{RunID: runID})
	if err != nil {
		t.Fatalf("Report() error = %v", err)
	}
	if first.Page.Matched != 1 {
		t.Fatalf("matched = %d, want 1", first.Page.Matched)
	}

	report = sampleReport(runID, []diagnostic.Diagnostic{minimalDiagnostic(0), minimalDiagnostic(1)})
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	writeFilesAt(t, store, runID, map[string][]byte{valgrind.AnalysisArtifactName: data})

	second, err := service.Report(context.Background(), ReportRequest{RunID: runID})
	if err != nil {
		t.Fatalf("Report() error = %v", err)
	}
	if second.Page.Matched != 2 {
		t.Errorf("matched after report change = %d, want 2 (stale cache?)", second.Page.Matched)
	}
}
