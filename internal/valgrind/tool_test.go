package valgrind

import (
	"strings"
	"testing"

	"github.com/Morditux/traceanvil-mcp/internal/diagnostic"
)

func TestBuildReportConcurrencyTools(t *testing.T) {
	for _, test := range []struct {
		tool Tool
		kind string
		sev  diagnostic.Severity
	}{
		{tool: ToolHelgrind, kind: "Race", sev: diagnostic.SeverityError},
		{tool: ToolDRD, kind: "ConflictingAccess", sev: diagnostic.SeverityError},
	} {
		t.Run(string(test.tool), func(t *testing.T) {
			xmlText := `<valgrindoutput><protocolversion>6</protocolversion><protocoltool>` +
				string(test.tool) + `</protocoltool><tool>` + string(test.tool) +
				`</tool><status><state>FINISHED</state></status><errorcounts><pair><count>1</count><unique>0x1</unique></pair></errorcounts><error_summary><errors>1</errors><suppressed>0</suppressed></error_summary><error><unique>0x1</unique><kind>` +
				test.kind + `</kind><what>conflict</what><stack><frame><fn>worker</fn><obj>/ws/fixture</obj><dir>/ws</dir><file>race.c</file><line>9</line></frame></stack></error></valgrindoutput>`
			parsed, err := Parse(strings.NewReader(xmlText), ParseOptions{})
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			report := BuildReport(BuildInput{
				RunID: "run_000000000000000000000000000000aa",
				Tool:  test.tool,
				Roots: []string{"/ws"},
				Options: Options{
					Tool: test.tool, NumCallers: DefaultNumCallers,
				},
				XML:     parsed,
				Program: diagnostic.Program{TargetID: "fixture"},
			})
			if !report.AnalysisComplete {
				t.Fatalf("report = %+v", report)
			}
			if len(report.Diagnostics) != 1 {
				t.Fatalf("diagnostics = %+v", report.Diagnostics)
			}
			item := report.Diagnostics[0]
			if item.Origin != test.tool.Origin() || item.Kind != test.kind || item.Severity != test.sev {
				t.Errorf("diagnostic = %+v", item)
			}
			if item.Evidence.Artifact != test.tool.RawArtifactName() {
				t.Errorf("artifact = %q, want %q", item.Evidence.Artifact, test.tool.RawArtifactName())
			}
			if value, _ := report.Options["tool"].(string); value != string(test.tool) {
				t.Errorf("options.tool = %q", value)
			}
		})
	}
}

func TestConcurrencySeverityPolicyPerKind(t *testing.T) {
	cases := []struct {
		tool Tool
		kind string
		want diagnostic.Severity
	}{
		{ToolHelgrind, "Race", diagnostic.SeverityError},
		{ToolHelgrind, "LockOrder", diagnostic.SeverityError},
		{ToolHelgrind, "UnlockUnlocked", diagnostic.SeverityWarning},
		{ToolHelgrind, "Misc", diagnostic.SeverityWarning},
		{ToolHelgrind, "Unknown", diagnostic.SeverityWarning},
		{ToolDRD, "ConflictingAccess", diagnostic.SeverityError},
		{ToolDRD, "MutexErr", diagnostic.SeverityWarning},
		{ToolDRD, "OtherErr", diagnostic.SeverityWarning},
		{ToolDRD, "Unknown", diagnostic.SeverityWarning},
	}
	for _, test := range cases {
		if got := test.tool.SeverityForKind(test.kind); got != test.want {
			t.Errorf("%s/%s severity = %q, want %q", test.tool, test.kind, got, test.want)
		}
	}
}

func TestClassifyRejectsMismatchedConcurrencyXML(t *testing.T) {
	snapshot := completedSnapshot()
	xml := &XMLResult{
		ProtocolTool: "memcheck", Tool: "memcheck",
		ErrorSummary: &XMLErrorSummary{},
	}
	result, complete, warnings := classifyAnalysis(snapshot, xml, ToolHelgrind, nil, nil, false, true)
	if result != diagnostic.ResultToolError || complete {
		t.Fatalf("classification = %q/%t", result, complete)
	}
	if len(warnings) == 0 || !strings.Contains(warnings[0], "not helgrind") {
		t.Errorf("warnings = %v", warnings)
	}
}
