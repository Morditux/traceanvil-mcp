package valgrind

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixturePath(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join("..", "..", "testdata", "fixtures", "valgrind", name)
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(fixturePath(t, name))
	if err != nil {
		t.Fatalf("read fixture %q: %v", name, err)
	}
	return data
}

func parseFixture(t *testing.T, name string) (*XMLResult, error) {
	t.Helper()
	file, err := os.Open(fixturePath(t, name))
	if err != nil {
		t.Fatalf("open fixture %q: %v", name, err)
	}
	defer file.Close()
	return Parse(file, ParseOptions{})
}

func TestParseInvalidRead(t *testing.T) {
	result, err := parseFixture(t, "invalid_read.xml")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if !result.Complete || result.Truncated {
		t.Errorf("Complete=%t Truncated=%t, want a complete document", result.Complete, result.Truncated)
	}
	if result.ProtocolVersion != 6 || result.ProtocolTool != "memcheck" || result.Tool != "memcheck" {
		t.Errorf("protocol = %d/%q tool=%q", result.ProtocolVersion, result.ProtocolTool, result.Tool)
	}
	if result.ErrorSummary == nil || result.ErrorSummary.Errors != 1 {
		t.Fatalf("ErrorSummary = %+v, want 1 error", result.ErrorSummary)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("len(Errors) = %d, want 1", len(result.Errors))
	}
	record := result.Errors[0]
	if record.Kind != "InvalidRead" || !strings.Contains(record.What, "Invalid read of size 4") {
		t.Errorf("error = %+v, want InvalidRead", record)
	}
	if len(record.Stack) != 1 {
		t.Fatalf("len(Stack) = %d, want 1", len(record.Stack))
	}
	if record.Stack[0].Function != "main" || record.Stack[0].File != "invalid_read.c" || record.Stack[0].Line != 8 {
		t.Errorf("top frame = %+v, want main at invalid_read.c:8", record.Stack[0])
	}
	if len(record.Contexts) == 0 || len(record.Contexts[0].Frames) != 2 {
		t.Errorf("Contexts = %+v, want an allocation stack", record.Contexts)
	}
	if record.Contexts[0].Label == "" {
		t.Error("allocation context has no auxwhat label")
	}
	if result.FinalState != "FINISHED" {
		t.Errorf("FinalState = %q, want FINISHED", result.FinalState)
	}
}

func TestParseRepeatedErrors(t *testing.T) {
	result, err := parseFixture(t, "repeat.xml")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("len(Errors) = %d, want one coalesced record", len(result.Errors))
	}
	if len(result.ErrorCounts) != 1 || result.ErrorCounts[0].Count != 3 {
		t.Fatalf("ErrorCounts = %+v, want one pair with count 3", result.ErrorCounts)
	}
	if result.ErrorSummary.Errors != 3 {
		t.Errorf("ErrorSummary.Errors = %d, want 3", result.ErrorSummary.Errors)
	}
}

func TestParseLeakKinds(t *testing.T) {
	result, err := parseFixture(t, "leak.xml")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(result.Errors) != 2 {
		t.Fatalf("len(Errors) = %d, want 2", len(result.Errors))
	}
	first := result.Errors[0]
	if first.Kind != "Leak_DefinitelyLost" || first.LeakedBytes != 8 || first.LeakedBlocks != 1 {
		t.Errorf("first leak = %+v", first)
	}
	if !strings.Contains(first.XWhat, "definitely lost") {
		t.Errorf("XWhat = %q, want the tool-reported leak text", first.XWhat)
	}
	second := result.Errors[1]
	if second.Kind != "Leak_StillReachable" || second.LeakedBytes != 16 {
		t.Errorf("second leak = %+v", second)
	}
}

func TestParseNoErrors(t *testing.T) {
	result, err := parseFixture(t, "no_errors.xml")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if !result.Complete {
		t.Error("Complete = false, want true")
	}
	if result.ErrorSummary == nil || result.ErrorSummary.Errors != 0 {
		t.Errorf("ErrorSummary = %+v, want zero errors", result.ErrorSummary)
	}
	if result.HeapSummary == nil || result.HeapSummary.BytesAllocated != 0 {
		t.Errorf("HeapSummary = %+v, want parsed zero counters", result.HeapSummary)
	}
	if result.AllHeapBlocksFree == nil || !*result.AllHeapBlocksFree {
		t.Errorf("AllHeapBlocksFree = %v, want true", result.AllHeapBlocksFree)
	}
}

func TestParseSuppressions(t *testing.T) {
	result, err := parseFixture(t, "suppressed.xml")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(result.Suppressions) != 1 {
		t.Fatalf("Suppressions = %+v, want one", result.Suppressions)
	}
	if result.Suppressions[0].Name != "ignore_addr4_main" || result.Suppressions[0].Count != 1 {
		t.Errorf("Suppression = %+v", result.Suppressions[0])
	}
	if result.ErrorSummary.Suppressed != 1 || result.ErrorSummary.Errors != 0 {
		t.Errorf("ErrorSummary = %+v", result.ErrorSummary)
	}
}

func TestParseOriginStack(t *testing.T) {
	result, err := parseFixture(t, "uninit.xml")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("len(Errors) = %d, want 1", len(result.Errors))
	}
	record := result.Errors[0]
	if record.Kind != "UninitCondition" {
		t.Errorf("Kind = %q, want UninitCondition", record.Kind)
	}
	if len(record.Contexts) != 1 || len(record.Contexts[0].Frames) != 1 {
		t.Errorf("Contexts = %+v, want the uninitialised-value stack", record.Contexts)
	}
	if record.Contexts[0].Frames[0].Line != 3 {
		t.Errorf("context top line = %d, want 3", record.Contexts[0].Frames[0].Line)
	}
}

func TestParseTruncated(t *testing.T) {
	result, err := parseFixture(t, "truncated.xml")
	if err == nil {
		t.Fatal("Parse() error = nil, want a truncated-document error")
	}
	if result.Complete {
		t.Error("Complete = true, want false")
	}
	if !result.Truncated {
		t.Error("Truncated = false, want true")
	}
	if len(result.Errors) == 0 {
		t.Error("partial diagnostics were discarded; want the records parsed before truncation")
	}
}

func TestParseMalformed(t *testing.T) {
	result, err := parseFixture(t, "malformed.xml")
	if err == nil {
		t.Fatal("Parse() error = nil, want a syntax error")
	}
	if result.Complete {
		t.Error("Complete = true, want false")
	}
}

func TestParseEmpty(t *testing.T) {
	result, err := parseFixture(t, "empty.xml")
	if err == nil {
		t.Fatal("Parse() error = nil, want an empty-document error")
	}
	if result.Complete {
		t.Error("Complete = true, want false")
	}
}

func TestParseUnknownFields(t *testing.T) {
	result, err := parseFixture(t, "unknown_fields.xml")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if !result.Complete || len(result.Errors) != 1 {
		t.Errorf("Complete=%t errors=%d, want complete with one error", result.Complete, len(result.Errors))
	}
}

func TestParseDepthLimit(t *testing.T) {
	var builder strings.Builder
	builder.WriteString("<valgrindoutput>")
	for range 20 {
		builder.WriteString("<a>")
	}
	for range 20 {
		builder.WriteString("</a>")
	}
	builder.WriteString("</valgrindoutput>")

	result, err := Parse(strings.NewReader(builder.String()), ParseOptions{MaxDepth: 8})
	if err == nil {
		t.Fatal("Parse() error = nil, want depth-limit error")
	}
	if !result.Truncated {
		t.Error("Truncated = false, want true")
	}
}

func TestParseDiagnosticLimit(t *testing.T) {
	result, err := parseFixtureWithOptions(t, "leak.xml", ParseOptions{MaxDiagnostics: 1})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(result.Errors) != 1 || result.DroppedDiagnostics != 1 {
		t.Errorf("Errors=%d Dropped=%d, want 1 and 1", len(result.Errors), result.DroppedDiagnostics)
	}
	if !result.Truncated {
		t.Error("Truncated = false, want true")
	}
}

func TestParseStringLimit(t *testing.T) {
	result, err := parseFixtureWithOptions(t, "invalid_read.xml", ParseOptions{MaxStringBytes: 8})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if !result.Truncated {
		t.Error("Truncated = false, want true after the string limit")
	}
	if len(result.Warnings) == 0 {
		t.Error("Warnings is empty, want a limit warning")
	}
}

func TestParseByteLimit(t *testing.T) {
	data := readFixture(t, "invalid_read.xml")
	result, err := Parse(strings.NewReader(string(data)), ParseOptions{MaxBytes: 256})
	if err == nil {
		t.Fatal("Parse() error = nil, want a byte-limit failure")
	}
	if !result.Truncated {
		t.Error("Truncated = false, want true")
	}
	if result.BytesRead > 257 {
		t.Errorf("BytesRead = %d, want bounded by MaxBytes+1", result.BytesRead)
	}
}

func parseFixtureWithOptions(t *testing.T, name string, opts ParseOptions) (*XMLResult, error) {
	t.Helper()
	file, err := os.Open(fixturePath(t, name))
	if err != nil {
		t.Fatalf("open fixture %q: %v", name, err)
	}
	defer file.Close()
	return Parse(file, opts)
}

func FuzzParse(f *testing.F) {
	for _, name := range []string{"invalid_read.xml", "truncated.xml", "leak.xml", "malformed.xml"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", "valgrind", name))
		if err != nil {
			f.Fatalf("read seed %q: %v", name, err)
		}
		f.Add(data)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			return
		}
		if _, err := Parse(strings.NewReader(string(data)), ParseOptions{MaxBytes: 1 << 20}); err != nil {
			// Malformed input is expected; the parser must not panic.
			return
		}
	})
}

func TestParseOriginElement(t *testing.T) {
	const document = `<?xml version="1.0"?>
<valgrindoutput>
<protocolversion>6</protocolversion>
<protocoltool>memcheck</protocoltool>
<tool>memcheck</tool>
<error>
  <unique>0x2</unique>
  <kind>UninitValue</kind>
  <what>Use of uninitialised value</what>
  <stack>
    <frame><obj>/ws/fixture</obj><fn>main</fn><dir>/ws</dir><file>main.c</file><line>9</line></frame>
  </stack>
  <origin>
    <what>Uninitialised value was created by a stack allocation</what>
    <stack>
      <frame><obj>/ws/fixture</obj><fn>helper</fn><dir>/ws</dir><file>main.c</file><line>3</line></frame>
    </stack>
  </origin>
</error>
<status><state>FINISHED</state></status>
<errorcounts><pair><count>1</count><unique>0x2</unique></pair></errorcounts>
<suppcounts></suppcounts>
<error_summary><errors>1</errors><error_contexts>1</error_contexts><suppressed>0</suppressed></error_summary>
</valgrindoutput>`

	result, err := Parse(strings.NewReader(document), ParseOptions{})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("len(Errors) = %d, want 1", len(result.Errors))
	}
	record := result.Errors[0]
	if len(record.Origin) != 1 || record.Origin[0].Function != "helper" || record.Origin[0].Line != 3 {
		t.Errorf("Origin = %+v, want the helper frame", record.Origin)
	}
	if len(record.Stack) != 1 || record.Stack[0].Function != "main" {
		t.Errorf("Stack = %+v", record.Stack)
	}
}

func TestParseTotalFrameLimit(t *testing.T) {
	const document = `<valgrindoutput>
<protocolversion>6</protocolversion><protocoltool>memcheck</protocoltool><tool>memcheck</tool>
<error><kind>A</kind><stack><frame><fn>a</fn></frame></stack></error>
<error><kind>B</kind><stack><frame><fn>b</fn></frame></stack></error>
<error><kind>C</kind><stack><frame><fn>c</fn></frame></stack></error>
<status><state>FINISHED</state></status>
<errorcounts></errorcounts>
<suppcounts></suppcounts>
<error_summary><errors>3</errors><error_contexts>3</error_contexts><suppressed>0</suppressed></error_summary>
</valgrindoutput>`
	result, err := Parse(strings.NewReader(document), ParseOptions{MaxFramesTotal: 2})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if !result.Truncated {
		t.Error("Truncated = false, want true after the total frame limit")
	}
	if len(result.Errors) != 3 {
		t.Errorf("len(Errors) = %d, want all three records retained", len(result.Errors))
	}
	last := result.Errors[len(result.Errors)-1]
	if len(last.Stack) != 0 {
		t.Errorf("last stack = %+v, want the excess frame dropped", last.Stack)
	}
}

func TestParseInvalidNumberIsNotTruncation(t *testing.T) {
	doc := `<?xml version="1.0"?><valgrindoutput><protocolversion>4</protocolversion><protocoltool>memcheck</protocoltool>` +
		`<errorcounts><pair><count>abc</count><unique>0x1</unique></pair></errorcounts></valgrindoutput>`
	result, err := Parse(strings.NewReader(doc), ParseOptions{})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if result.Truncated || !result.Invalid || result.Complete {
		t.Errorf("Truncated=%t Invalid=%t Complete=%t, want invalid and incomplete only", result.Truncated, result.Invalid, result.Complete)
	}
}
