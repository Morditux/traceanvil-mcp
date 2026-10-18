package diagnostic

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func diffDiagnostic(id, fingerprint string, occurrences int) Diagnostic {
	return Diagnostic{
		ID:                   id,
		Fingerprint:          "sha256:" + strings.Repeat(fingerprint, 64/len(fingerprint)),
		FingerprintAlgorithm: FingerprintAlgorithmV2,
		Origin:               "valgrind-memcheck",
		Kind:                 "InvalidRead",
		Severity:             SeverityError,
		Message:              "Invalid read of size 4",
		Location:             &Location{Function: "main", Path: "fixture.c", Line: 8},
		Stack:                []Frame{{Function: "main", Path: "fixture.c", Line: 8}},
		Occurrences:          occurrences,
		Evidence:             Evidence{Artifact: "memcheck.xml", RecordID: id, RecordIndex: 0},
		Confidence:           ConfidenceToolReported,
	}
}

func diffReport(runID string, diagnostics []Diagnostic) Report {
	return Report{
		SchemaVersion:    SchemaVersion,
		RunID:            runID,
		Tool:             Tool{Name: "valgrind", Path: "/usr/bin/valgrind", Version: "valgrind-3.26.0"},
		Result:           ResultErrorsFound,
		AnalysisComplete: true,
		Program: Program{
			TargetID: "fixture", Path: "/workspace/fixture",
			SHA256: "sha256:" + strings.Repeat("cd", 32), Args: []string{}, ExitCode: 42,
		},
		Metadata: Metadata{
			OS: "linux", Arch: "amd64", WorkspaceRoots: []string{"/workspace"},
			FingerprintAlgorithm: FingerprintAlgorithmV2, ParserVersion: "1",
		},
		Options:     map[string]any{"leak_check": "full"},
		Summary:     BuildSummary(diagnostics, nil, false),
		Diagnostics: diagnostics,
		Artifacts:   []Artifact{},
		Warnings:    []string{},
	}
}

func TestCompareClassifications(t *testing.T) {
	persistent := diffDiagnostic("base-persistent", "aa", 1)
	resolved := diffDiagnostic("base-resolved", "bb", 1)
	changed := diffDiagnostic("base-changed", "cc", 1)
	base := diffReport("run_00000000000000000000000000000001", []Diagnostic{persistent, resolved, changed})

	persistentTarget := diffDiagnostic("target-persistent", "aa", 1)
	changedTarget := diffDiagnostic("target-changed", "cc", 3)
	newItem := diffDiagnostic("target-new", "dd", 1)
	target := diffReport("run_00000000000000000000000000000002", []Diagnostic{persistentTarget, changedTarget, newItem})

	result, err := Compare(base, target)
	if err != nil {
		t.Fatalf("Compare() error = %v", err)
	}
	if result.Summary.New != 1 || result.Summary.Resolved != 1 ||
		result.Summary.Persistent != 1 || result.Summary.Changed != 1 || result.Summary.Uncertain != 0 {
		t.Fatalf("summary = %+v", result.Summary)
	}
	changes := make(map[Change]DiffItem, len(result.Items))
	for _, item := range result.Items {
		changes[item.Change] = item
	}
	if item := changes[ChangeNew]; item.Base != nil || item.Target == nil {
		t.Errorf("new item = %+v", item)
	}
	if item := changes[ChangeResolved]; item.Base == nil || item.Target != nil {
		t.Errorf("resolved item = %+v", item)
	}
	if item := changes[ChangePersistent]; item.Base == nil || item.Target == nil {
		t.Errorf("persistent item = %+v", item)
	}
	if item := changes[ChangeChanged]; item.Base.Occurrences != 1 || item.Target.Occurrences != 3 {
		t.Errorf("changed item = %+v", item)
	}
	if result.Compatibility.FingerprintAlgorithm != FingerprintAlgorithmV2 {
		t.Errorf("algorithm = %q", result.Compatibility.FingerprintAlgorithm)
	}
}

func TestCompareDuplicateFingerprintIsUncertain(t *testing.T) {
	base := diffReport("run_00000000000000000000000000000003", []Diagnostic{
		diffDiagnostic("base-1", "ee", 1),
		diffDiagnostic("base-2", "ee", 1),
	})
	target := diffReport("run_00000000000000000000000000000004", []Diagnostic{
		diffDiagnostic("target-1", "ee", 1),
	})
	result, err := Compare(base, target)
	if err != nil {
		t.Fatalf("Compare() error = %v", err)
	}
	if result.Summary.Uncertain != 1 || result.Summary.Persistent != 0 {
		t.Fatalf("summary = %+v", result.Summary)
	}
	item := result.Items[0]
	if item.Change != ChangeUncertain || item.BaseCount != 2 || item.TargetCount != 1 {
		t.Errorf("item = %+v", item)
	}
	if !strings.Contains(strings.Join(item.Reasons, " "), "duplicate") {
		t.Errorf("reasons = %v", item.Reasons)
	}
}

func TestCompareConfigurationChangeMakesItemsUncertain(t *testing.T) {
	base := diffReport("run_00000000000000000000000000000005", []Diagnostic{diffDiagnostic("base", "ff", 1)})
	target := diffReport("run_00000000000000000000000000000006", []Diagnostic{diffDiagnostic("target", "ff", 1)})
	target.Options = map[string]any{"leak_check": "summary"}
	result, err := Compare(base, target)
	if err != nil {
		t.Fatalf("Compare() error = %v", err)
	}
	if len(result.Compatibility.Changes) == 0 {
		t.Fatal("configuration change was not reported")
	}
	if result.Summary.Uncertain != 1 || result.Summary.Persistent != 0 {
		t.Fatalf("summary = %+v", result.Summary)
	}
	if result.Items[0].Change != ChangeUncertain {
		t.Errorf("item = %+v", result.Items[0])
	}
}

func TestCompareIncompleteReportMakesItemsUncertain(t *testing.T) {
	base := diffReport("run_00000000000000000000000000000007", []Diagnostic{diffDiagnostic("base", "11", 1)})
	target := diffReport("run_00000000000000000000000000000008", []Diagnostic{})
	target.Result = ResultIncomplete
	target.AnalysisComplete = false
	target.Summary = BuildSummary(nil, nil, false)
	result, err := Compare(base, target)
	if err != nil {
		t.Fatalf("Compare() error = %v", err)
	}
	if len(result.Compatibility.Uncertainties) == 0 {
		t.Fatal("incomplete report was not flagged")
	}
	if result.Summary.Uncertain != 1 || result.Summary.Resolved != 0 {
		t.Fatalf("summary = %+v", result.Summary)
	}
}

func TestCompareIncompatibleMetadataFails(t *testing.T) {
	base := diffReport("run_00000000000000000000000000000009", []Diagnostic{diffDiagnostic("base", "22", 1)})
	target := diffReport("run_0000000000000000000000000000000a", []Diagnostic{diffDiagnostic("target", "22", 1)})
	target.Tool.Name = "helgrind"
	_, err := Compare(base, target)
	var incompatible *IncompatibleError
	if !errors.As(err, &incompatible) {
		t.Fatalf("Compare() error = %v, want IncompatibleError", err)
	}
	if len(incompatible.Reasons) == 0 {
		t.Fatal("incompatible error has no reasons")
	}

	target = diffReport("run_0000000000000000000000000000000b", []Diagnostic{diffDiagnostic("target", "22", 1)})
	target.Metadata.FingerprintAlgorithm = FingerprintAlgorithmV1
	target.Diagnostics[0].FingerprintAlgorithm = FingerprintAlgorithmV1
	if _, err := Compare(base, target); !errors.As(err, &incompatible) {
		t.Fatalf("algorithm mismatch error = %v", err)
	}

	target = diffReport("run_0000000000000000000000000000000c", []Diagnostic{})
	target.Result = ResultToolError
	target.AnalysisComplete = false
	target.Summary = BuildSummary(nil, nil, false)
	if _, err := Compare(base, target); !errors.As(err, &incompatible) {
		t.Fatalf("tool_error error = %v", err)
	}
}

func TestCompareSuppressionChangeIsUncertain(t *testing.T) {
	base := diffReport("run_0000000000000000000000000000000d", []Diagnostic{diffDiagnostic("base", "33", 1)})
	base.Suppressions = []Suppression{{Name: "known", Count: 2}}
	target := diffReport("run_0000000000000000000000000000000e", []Diagnostic{diffDiagnostic("target", "33", 1)})
	result, err := Compare(base, target)
	if err != nil {
		t.Fatalf("Compare() error = %v", err)
	}
	if len(result.Compatibility.Uncertainties) == 0 || result.Summary.Uncertain != 1 {
		t.Fatalf("result = %+v", result)
	}
}

func TestFingerprintV2IgnoresLineButNotFunctionOrPath(t *testing.T) {
	stack := []Frame{
		{Function: "malloc", Path: "/usr/libexec/valgrind/vgpreload_memcheck.so", Runtime: true},
		{Function: "main", Path: "src/main.c", Line: 12},
	}
	first := FingerprintV2("valgrind-memcheck", "InvalidRead", stack)
	if first != FingerprintV2("valgrind-memcheck", "InvalidRead", stack) {
		t.Fatal("v2 fingerprint is not stable")
	}
	lineMoved := append([]Frame(nil), stack...)
	lineMoved[1].Line = 99
	if first != FingerprintV2("valgrind-memcheck", "InvalidRead", lineMoved) {
		t.Error("v2 fingerprint depends on a source line")
	}
	functionChanged := append([]Frame(nil), stack...)
	functionChanged[1].Function = "helper"
	if first == FingerprintV2("valgrind-memcheck", "InvalidRead", functionChanged) {
		t.Error("v2 fingerprint does not depend on a function")
	}
	pathChanged := append([]Frame(nil), stack...)
	pathChanged[1].Path = "src/other.c"
	if first == FingerprintV2("valgrind-memcheck", "InvalidRead", pathChanged) {
		t.Error("v2 fingerprint does not depend on a path")
	}
	if first == FingerprintV1("valgrind-memcheck", "InvalidRead", stack) {
		t.Error("v1 and v2 recipes produced the same digest")
	}
}

func TestCompareV1ReportsRemainSupported(t *testing.T) {
	base := diffReport("run_0000000000000000000000000000000f", []Diagnostic{diffDiagnostic("base", "44", 1)})
	target := diffReport("run_00000000000000000000000000000010", []Diagnostic{diffDiagnostic("target", "44", 1)})
	for _, report := range []*Report{&base, &target} {
		report.Metadata.FingerprintAlgorithm = FingerprintAlgorithmV1
		for i := range report.Diagnostics {
			report.Diagnostics[i].FingerprintAlgorithm = FingerprintAlgorithmV1
		}
	}
	result, err := Compare(base, target)
	if err != nil {
		t.Fatalf("Compare(v1) error = %v", err)
	}
	if result.Compatibility.FingerprintAlgorithm != FingerprintAlgorithmV1 || result.Summary.Persistent != 1 {
		t.Fatalf("result = %+v", result)
	}
}

func TestCompareBinaryChangeIsReportedButNotDowngraded(t *testing.T) {
	base := diffReport("run_00000000000000000000000000000011", []Diagnostic{diffDiagnostic("base", "55", 1)})
	target := diffReport("run_00000000000000000000000000000012", []Diagnostic{diffDiagnostic("target", "55", 1)})
	target.Program.SHA256 = "sha256:" + strings.Repeat("ef", 32)
	target.Program.ExitCode = 0
	target.Program.Path = "/workspace/fixture-new"
	result, err := Compare(base, target)
	if err != nil {
		t.Fatalf("Compare() error = %v", err)
	}
	if len(result.Compatibility.Changes) == 0 {
		t.Fatal("binary/outcome change was not reported")
	}
	if len(result.Compatibility.Uncertainties) != 0 {
		t.Fatalf("binary change incorrectly downgraded the comparison: %v", result.Compatibility.Uncertainties)
	}
	if result.Summary.Persistent != 1 || result.Summary.Uncertain != 0 {
		t.Fatalf("summary = %+v", result.Summary)
	}
}

func TestIncompatibleReasonsAreBounded(t *testing.T) {
	baseDiagnostics := make([]Diagnostic, 100)
	targetDiagnostics := make([]Diagnostic, 100)
	for i := range baseDiagnostics {
		baseDiagnostics[i] = diffDiagnostic(fmt.Sprintf("base-%d", i), "66", 1)
		baseDiagnostics[i].FingerprintAlgorithm = FingerprintAlgorithmV1
		targetDiagnostics[i] = diffDiagnostic(fmt.Sprintf("target-%d", i), "66", 1)
	}
	base := diffReport("run_00000000000000000000000000000013", baseDiagnostics)
	target := diffReport("run_00000000000000000000000000000014", targetDiagnostics)
	base.Metadata.FingerprintAlgorithm = ""
	target.Metadata.FingerprintAlgorithm = ""
	_, err := Compare(base, target)
	var incompatible *IncompatibleError
	if !errors.As(err, &incompatible) {
		t.Fatalf("Compare() error = %v, want IncompatibleError", err)
	}
	if len(incompatible.Reasons) > 16 {
		t.Fatalf("reasons = %d, want a bounded list", len(incompatible.Reasons))
	}
	if len(incompatible.Error()) > 16*512+128 {
		t.Fatalf("error is too large: %d bytes", len(incompatible.Error()))
	}
}

func TestCompareSuppressionComparisonIsSymmetric(t *testing.T) {
	base := diffReport("run_00000000000000000000000000000015", []Diagnostic{diffDiagnostic("base", "77", 1)})
	base.Suppressions = []Suppression{{Name: "a", Count: 1}, {Name: "b", Count: 1}}
	target := diffReport("run_00000000000000000000000000000016", []Diagnostic{diffDiagnostic("target", "77", 1)})
	target.Suppressions = []Suppression{{Name: "a", Count: 1}, {Name: "a", Count: 1}}
	result, err := Compare(base, target)
	if err != nil {
		t.Fatalf("Compare() error = %v", err)
	}
	if !containsText(result.Compatibility.Uncertainties, "suppression") {
		t.Fatalf("uncertainties = %v, want a suppression difference", result.Compatibility.Uncertainties)
	}
}

func containsText(values []string, substring string) bool {
	for _, value := range values {
		if strings.Contains(value, substring) {
			return true
		}
	}
	return false
}

func TestCompareDifferentAnalysisToolsAreIncompatible(t *testing.T) {
	base := diffReport("run_0000000000000000000000000000000d", []Diagnostic{diffDiagnostic("base", "44", 1)})
	base.Options["tool"] = "memcheck"
	target := diffReport("run_0000000000000000000000000000000e", []Diagnostic{diffDiagnostic("target", "44", 1)})
	target.Options["tool"] = "helgrind"
	_, err := Compare(base, target)
	var incompatible *IncompatibleError
	if !errors.As(err, &incompatible) {
		t.Fatalf("Compare() error = %v, want IncompatibleError", err)
	}
	if !strings.Contains(strings.Join(incompatible.Reasons, " "), "analysis tools differ") {
		t.Errorf("reasons = %v", incompatible.Reasons)
	}
}
