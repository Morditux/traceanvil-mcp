package diagnostic

import (
	"strings"
	"testing"
)

func TestNormalizePath(t *testing.T) {
	roots := []string{"/home/dev/project", "/srv/other"}
	cases := []struct {
		path string
		want string
	}{
		{"/home/dev/project/src/main.c", "src/main.c"},
		{"/home/dev/project", "."},
		{"/srv/other/build/../src/a.c", "src/a.c"},
		{"/opt/third-party/lib.c", "/opt/third-party/lib.c"},
		{"relative/file.c", "relative/file.c"},
		{"/home/dev/project/with space-é/file.c", "with space-é/file.c"},
		{"", ""},
	}
	for _, test := range cases {
		if got := NormalizePath(test.path, roots); got != test.want {
			t.Errorf("NormalizePath(%q) = %q, want %q", test.path, got, test.want)
		}
	}
}

func TestSelectLocationSkipsRuntimeFrames(t *testing.T) {
	stack := []Frame{
		{Function: "malloc", Path: "/usr/src/valgrind/vg_replace_malloc.c", Line: 447, Runtime: true},
		{Function: "main", Path: "src/main.c", Line: 12},
	}
	location := SelectLocation(stack)
	if location == nil || location.Function != "main" || location.Path != "src/main.c" || location.Line != 12 {
		t.Errorf("SelectLocation() = %+v", location)
	}

	if location := SelectLocation([]Frame{{Function: "vmalloc", Runtime: true}}); location != nil {
		t.Errorf("SelectLocation() = %+v, want nil when only runtime frames exist", location)
	}
	if location := SelectLocation(nil); location != nil {
		t.Errorf("SelectLocation(nil) = %+v, want nil", location)
	}
}

func TestBuildSummary(t *testing.T) {
	diagnostics := []Diagnostic{
		{Kind: "InvalidRead", Severity: SeverityError, Occurrences: 3, LeakedBytes: 0},
		{Kind: "Leak_DefinitelyLost", Severity: SeverityError, Occurrences: 1, LeakedBytes: 8, LeakedBlocks: 1},
	}
	suppressions := []Suppression{{Name: "addr4", Count: 2}}
	summary := BuildSummary(diagnostics, suppressions, true)
	if summary.Total != 4 || summary.DiagnosticRecords != 2 {
		t.Errorf("Total=%d Records=%d, want 4 and 2", summary.Total, summary.DiagnosticRecords)
	}
	if summary.ByKind["InvalidRead"] != 3 || summary.ByKind["Leak_DefinitelyLost"] != 1 {
		t.Errorf("ByKind = %+v", summary.ByKind)
	}
	if summary.BySeverity["error"] != 4 {
		t.Errorf("BySeverity = %+v", summary.BySeverity)
	}
	if summary.LeakedBytes != 8 || summary.LeakedBlocks != 1 {
		t.Errorf("leaks = %d/%d", summary.LeakedBytes, summary.LeakedBlocks)
	}
	if summary.Suppressed != 2 || !summary.Truncated {
		t.Errorf("summary = %+v", summary)
	}
}

func TestFingerprintV1(t *testing.T) {
	stack := []Frame{
		{Function: "malloc", Path: "/usr/libexec/valgrind/vgpreload_memcheck.so", Runtime: true},
		{Function: "main", Path: "src/main.c", Line: 12},
	}
	first := FingerprintV1("valgrind-memcheck", "InvalidRead", stack)
	if !strings.HasPrefix(first, "sha256:") {
		t.Fatalf("fingerprint = %q", first)
	}
	if first != FingerprintV1("valgrind-memcheck", "InvalidRead", stack) {
		t.Error("fingerprint is not stable")
	}
	if first == FingerprintV1("valgrind-memcheck", "InvalidWrite", stack) {
		t.Error("fingerprint does not depend on kind")
	}

	changed := append([]Frame(nil), stack...)
	changed[1].Line = 13
	if first == FingerprintV1("valgrind-memcheck", "InvalidRead", changed) {
		t.Error("fingerprint does not depend on the source line")
	}

	// Runtime frames are excluded, so changing one does not change the digest.
	changed = append([]Frame(nil), stack...)
	changed[0].Function = "calloc"
	changed[0].Line = 999
	if first != FingerprintV1("valgrind-memcheck", "InvalidRead", changed) {
		t.Error("fingerprint depends on a runtime frame")
	}

	if FingerprintV1("origin", "Kind", nil) != FingerprintV1("origin", "Kind", []Frame{{Runtime: true}}) {
		t.Error("runtime-only and empty stacks should both use the no-stack recipe")
	}
}

func TestReportValidate(t *testing.T) {
	valid := Report{
		SchemaVersion:    SchemaVersion,
		RunID:            "run_0123456789abcdef0123456789abcdef",
		Tool:             Tool{Name: "valgrind"},
		Result:           ResultErrorsFound,
		AnalysisComplete: true,
		Summary:          Summary{Total: 1, DiagnosticRecords: 1},
		Diagnostics: []Diagnostic{{
			ID:                   "diag_1",
			Fingerprint:          "sha256:abc",
			FingerprintAlgorithm: FingerprintAlgorithmV1,
			Origin:               "valgrind-memcheck",
			Kind:                 "InvalidRead",
			Severity:             SeverityError,
			Confidence:           ConfidenceToolReported,
			Occurrences:          1,
		}},
	}
	if err := valid.Validate(); err != nil {
		t.Errorf("Validate() error = %v", err)
	}

	cases := map[string]func(*Report){
		"schema":            func(r *Report) { r.SchemaVersion = 0 },
		"run id":            func(r *Report) { r.RunID = "" },
		"tool name":         func(r *Report) { r.Tool.Name = "" },
		"result":            func(r *Report) { r.Result = "clean" },
		"no errors partial": func(r *Report) { r.Result = ResultNoErrorsObserved; r.AnalysisComplete = false },
		"errors without total": func(r *Report) {
			r.Summary.Total = 0
			r.Summary.DiagnosticRecords = 0
			r.Diagnostics = nil
		},
		"duplicate id":    func(r *Report) { r.Diagnostics = append(r.Diagnostics, r.Diagnostics[0]) },
		"empty kind":      func(r *Report) { r.Diagnostics[0].Kind = "" },
		"bad severity":    func(r *Report) { r.Diagnostics[0].Severity = "fatal" },
		"bad confidence":  func(r *Report) { r.Diagnostics[0].Confidence = "guess" },
		"bad fingerprint": func(r *Report) { r.Diagnostics[0].Fingerprint = "md5:abc" },
		"zero occurrences": func(r *Report) {
			r.Diagnostics[0].Occurrences = 0
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			report := valid
			report.Diagnostics = append([]Diagnostic(nil), valid.Diagnostics...)
			mutate(&report)
			if err := report.Validate(); err == nil {
				t.Fatal("Validate() accepted an invalid report")
			}
		})
	}
}
